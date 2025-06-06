// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/grafana/dskit/cancellation"
	"github.com/grafana/dskit/middleware"
	"github.com/grafana/dskit/multierror"
	"github.com/grafana/dskit/user"

	"github.com/grafana/mimir/pkg/mimirpb"
	util_log "github.com/grafana/mimir/pkg/util/log"
	"github.com/grafana/mimir/pkg/util/spanlogger"
)

type Pusher interface {
	PushToStorage(context.Context, *mimirpb.WriteRequest) error
}

type PusherCloser interface {
	// PushToStorage pushes the write request to the storage.
	PushToStorage(context.Context, *mimirpb.WriteRequest) error
	// Close tells the PusherCloser that no more records are coming and it should flush any remaining records.
	Close() []error
}

// pusherConsumer receives records from Kafka and pushes them to the storage.
// Each time a batch of records is received from Kafka, we instantiate a new pusherConsumer, this is to ensure we can retry if necessary and know whether we have completed that batch or not.
type pusherConsumer struct {
	metrics *pusherConsumerMetrics
	logger  log.Logger

	kafkaConfig KafkaConfig

	pusher Pusher
}

// newPusherConsumer creates a new pusherConsumer instance.
func newPusherConsumer(pusher Pusher, kafkaCfg KafkaConfig, metrics *pusherConsumerMetrics, logger log.Logger) *pusherConsumer {
	// The layer below (parallelStoragePusher, parallelStorageShards, sequentialStoragePusher) will return all errors they see
	// and potentially ingesting a batch if they encounter any error.
	// We can safely ignore client errors and continue ingesting. We abort ingesting if we get any other error.
	return &pusherConsumer{
		pusher:      pusher,
		kafkaConfig: kafkaCfg,
		metrics:     metrics,
		logger:      logger,
	}
}

// Consume implements the recordConsumer interface.
// It'll use a separate goroutine to unmarshal the next record while we push the current record to storage.
func (c pusherConsumer) Consume(ctx context.Context, records []record) (returnErr error) {
	defer func(processingStart time.Time) {
		c.metrics.processingTimeSeconds.Observe(time.Since(processingStart).Seconds())
	}(time.Now())

	type parsedRecord struct {
		*mimirpb.WriteRequest
		// ctx holds the tracing baggage for this record/request.
		ctx      context.Context
		tenantID string
		err      error
		index    int
	}

	recordsChannel := make(chan parsedRecord)

	// Create a cancellable context to let the unmarshalling goroutine know when to stop.
	ctx, cancel := context.WithCancelCause(ctx)

	// Now, unmarshal the records into the channel.
	go func(unmarshalCtx context.Context, records []record, ch chan<- parsedRecord) {
		defer close(ch)

		for index, r := range records {
			// Before we being unmarshalling the write request check if the context was cancelled.
			select {
			case <-unmarshalCtx.Done():
				// No more processing is needed, so we need to abort.
				return
			default:
			}

			parsed := parsedRecord{
				ctx:          r.ctx,
				tenantID:     r.tenantID,
				WriteRequest: &mimirpb.WriteRequest{},
				index:        index,
			}

			if r.version > LatestRecordVersion {
				parsed.err = fmt.Errorf("received a record with an unsupported version: %d, max supported version: %d", r.version, LatestRecordVersion)
			}

			// We don't free the WriteRequest slices because they are being freed by a level below.
			err := parsed.Unmarshal(r.content)
			if err != nil {
				parsed.err = fmt.Errorf("parsing ingest consumer write request: %w", err)
			}

			// Now that we're done, check again before we send it to the channel.
			select {
			case <-unmarshalCtx.Done():
				return
			case ch <- parsed:
			}
		}
	}(ctx, records, recordsChannel)

	// We accumulate the total bytes across all records per tenant to determine the number of timeseries we expected to receive.
	// Then, we'll use that to determine the number of shards we need to parallelize the writes.
	var bytesPerTenant = make(map[string]int)
	for _, r := range records {
		bytesPerTenant[r.tenantID] += len(r.content)
	}

	// Create and start the storage writer.
	writer := c.newStorageWriter(bytesPerTenant)

	// Ensure the writer gets closed either in case of successful or failed consumption.
	// If we don't close the writer, then we have a goroutines and memory leak.
	defer func() {
		writerCloseErrs := writer.Close()

		// Return writer close errors only if there was not already a returned error set (we don't want to overwrite it).
		if len(writerCloseErrs) > 0 && returnErr == nil {
			returnErr = multierror.New(writerCloseErrs...).Err()
		}
	}()

	for r := range recordsChannel {
		if r.err != nil {
			level.Error(spanlogger.FromContext(ctx, c.logger)).Log("msg", "failed to parse write request; skipping", "err", r.err)
			continue
		}

		// If we get an error at any point, we need to stop processing the records. They will be retried at some point.
		err := c.pushToStorage(r.ctx, r.tenantID, r.WriteRequest, writer)
		if err != nil {
			cancel(cancellation.NewErrorf("error while pushing to storage")) // Stop the unmarshalling goroutine.
			return fmt.Errorf("consuming record at index %d for tenant %s: %w", r.index, r.tenantID, err)
		}
	}

	cancel(cancellation.NewErrorf("done unmarshalling records"))
	return nil
}

func (c pusherConsumer) newStorageWriter(bytesPerTenant map[string]int) PusherCloser {
	if c.kafkaConfig.IngestionConcurrencyMax == 0 {
		return newSequentialStoragePusher(c.metrics.storagePusherMetrics, c.pusher, c.kafkaConfig.FallbackClientErrorSampleRate, c.logger)
	}

	return newParallelStoragePusher(
		c.metrics.storagePusherMetrics,
		c.pusher,
		bytesPerTenant,
		c.kafkaConfig.FallbackClientErrorSampleRate,
		c.kafkaConfig.IngestionConcurrencyMax,
		c.kafkaConfig.IngestionConcurrencyBatchSize,
		c.kafkaConfig.IngestionConcurrencyQueueCapacity,
		c.kafkaConfig.IngestionConcurrencyEstimatedBytesPerSample,
		c.kafkaConfig.IngestionConcurrencyTargetFlushesPerShard,
		c.logger,
	)
}

func (c pusherConsumer) pushToStorage(ctx context.Context, tenantID string, req *mimirpb.WriteRequest, writer PusherCloser) error {
	spanLog, ctx := spanlogger.NewWithLogger(ctx, c.logger, "pusherConsumer.pushToStorage")
	defer spanLog.Finish()

	// Note that the implementation of the Pusher expects the tenantID to be in the context.
	ctx = user.InjectOrgID(ctx, tenantID)

	err := writer.PushToStorage(ctx, req)

	return err
}

// sequentialStoragePusher receives mimirpb.WriteRequest which are then pushed to the storage one by one.
type sequentialStoragePusher struct {
	metrics      *storagePusherMetrics
	errorHandler *pushErrorHandler

	pusher Pusher
}

// newSequentialStoragePusher creates a new sequentialStoragePusher instance.
func newSequentialStoragePusher(metrics *storagePusherMetrics, pusher Pusher, sampleRate int64, logger log.Logger) sequentialStoragePusher {
	return sequentialStoragePusher{
		metrics:      metrics,
		pusher:       pusher,
		errorHandler: newPushErrorHandler(metrics, util_log.NewSampler(sampleRate), logger),
	}
}

func newSequentialStoragePusherWithErrorHandler(metrics *storagePusherMetrics, pusher Pusher, errorHandler *pushErrorHandler) sequentialStoragePusher {
	return sequentialStoragePusher{
		metrics:      metrics,
		pusher:       pusher,
		errorHandler: errorHandler,
	}
}

// PushToStorage implements the PusherCloser interface.
func (ssp sequentialStoragePusher) PushToStorage(ctx context.Context, wr *mimirpb.WriteRequest) error {
	ssp.metrics.timeSeriesPerFlush.Observe(float64(len(wr.Timeseries)))
	defer func(now time.Time) {
		ssp.metrics.processingTime.WithLabelValues(requestContents(wr)).Observe(time.Since(now).Seconds())
	}(time.Now())

	if err := ssp.pusher.PushToStorage(ctx, wr); ssp.errorHandler.IsServerError(ctx, err) {
		return err
	}

	return nil
}

// Close implements the PusherCloser interface.
func (ssp sequentialStoragePusher) Close() []error {
	return nil
}

// parallelStoragePusher receives WriteRequest which are then pushed to the storage in parallel.
// The parallelism is two-tiered which means that we first parallelize by tenantID and then by series.
type parallelStoragePusher struct {
	metrics *storagePusherMetrics
	logger  log.Logger

	// pushers is map["$tenant|$source"]*parallelStorageShards
	pushers        map[string]PusherCloser
	upstreamPusher Pusher
	errorHandler   *pushErrorHandler

	maxShards      int
	batchSize      int
	bytesPerTenant map[string]int

	queueCapacity   int
	bytesPerSample  int
	targetFlushes   int
	numActiveShards int
}

// newParallelStoragePusher creates a new parallelStoragePusher instance.
func newParallelStoragePusher(metrics *storagePusherMetrics, pusher Pusher, bytesPerTenant map[string]int, loggerSampleRate int64, maxShards int, batchSize int, queueCapacity int, bytesPerSample int, targetFlushes int, logger log.Logger) *parallelStoragePusher {
	return &parallelStoragePusher{
		logger:         log.With(logger, "component", "parallel-storage-pusher"),
		pushers:        make(map[string]PusherCloser),
		upstreamPusher: pusher,
		maxShards:      maxShards,
		bytesPerTenant: bytesPerTenant,
		errorHandler:   newPushErrorHandler(metrics, util_log.NewSampler(loggerSampleRate), logger),
		batchSize:      batchSize,
		queueCapacity:  queueCapacity,
		bytesPerSample: bytesPerSample,
		targetFlushes:  targetFlushes,
		metrics:        metrics,
	}
}

// PushToStorage implements the PusherCloser interface.
func (c *parallelStoragePusher) PushToStorage(ctx context.Context, wr *mimirpb.WriteRequest) error {
	userID, err := user.ExtractOrgID(ctx)
	if err != nil {
		level.Error(c.logger).Log("msg", "failed to extract tenant ID from context", "err", err)
	}

	shards := c.shardsFor(userID, wr.Source)
	return shards.PushToStorage(ctx, wr)
}

// Close implements the PusherCloser interface.
func (c *parallelStoragePusher) Close() []error {
	var errs multierror.MultiError
	for _, p := range c.pushers {
		errs = append(errs, p.Close()...)
	}
	c.metrics.shardsPerPush.Observe(float64(c.numActiveShards))
	c.metrics.pushersPerPush.Observe(float64(len(c.pushers)))
	clear(c.pushers)
	return errs
}

// shardsFor returns the parallelStorageShards for the given userID. Once created the same shards are re-used for the same userID.
// We create a shard for each tenantID to parallelize the writes.
func (c *parallelStoragePusher) shardsFor(userID string, requestSource mimirpb.WriteRequest_SourceEnum) PusherCloser {
	// Construct the string inline so that it doesn't escape to the heap. Go doesn't escape strings that are used to only look up map keys.
	// We can use "|" because that cannot be part of a tenantID in Mimir.
	if p := c.pushers[userID+"|"+requestSource.String()]; p != nil {
		return p
	}

	idealShards := c.idealShardsFor(userID)
	var p PusherCloser
	if idealShards <= 1 {
		// If we're going to push only one shard, then we can use the sequential pusher.
		// This means that pushes will now be synchronous.
		// The idea is that if we don't see a reason to parallelize,
		// then the pushes to this pusher are likely small in absolute terms and speeding them up will have marginal gains.
		// So we choose the lower overhead and simpler sequential pusher.
		p = newSequentialStoragePusherWithErrorHandler(c.metrics, c.upstreamPusher, c.errorHandler)
	} else {
		p = newParallelStorageShards(c.metrics, c.errorHandler, idealShards, c.batchSize, c.queueCapacity, c.upstreamPusher)
	}
	c.pushers[userID+"|"+requestSource.String()] = p
	return p
}

// idealShardsFor returns the number of shards that should be used for the given userID.
func (c *parallelStoragePusher) idealShardsFor(userID string) int {
	// First, determine the number of timeseries we expect to receive based on the bytes of WriteRequest's we received.
	expectedTimeseries := c.bytesPerTenant[userID] / c.bytesPerSample

	c.metrics.estimatedTimeseries.Add(float64(expectedTimeseries))

	// Then, determine the number of shards we should use to parallelize the writes.
	idealShards := expectedTimeseries / c.batchSize / c.targetFlushes

	// Finally, use the lower of the two as a conservative estimate.
	// The max(1, ...) is to ensure that we always have at least one shard.
	r := max(1, min(idealShards, c.maxShards))

	c.numActiveShards += r
	return r
}

// parallelStorageShards is a collection of shards that are used to parallelize the writes to the storage by series.
// Each series is hashed to a shard that contains its own batchingQueue.
// Each series is consistently assigned to the same shard. This allows us to preserve the order of samples of the same series between multiple PushToStorage calls.
type parallelStorageShards struct {
	metrics      *storagePusherMetrics
	errorHandler *pushErrorHandler

	pusher Pusher

	numShards int
	batchSize int
	capacity  int

	wg     *sync.WaitGroup
	shards []*batchingQueue
}

type flushableWriteRequest struct {
	// startedAt is the time when the first item was added to this request (timeseries or metadata).
	startedAt time.Time
	*mimirpb.WriteRequest
	context.Context
}

// newParallelStorageShards creates a new parallelStorageShards instance.
func newParallelStorageShards(metrics *storagePusherMetrics, errorHandler *pushErrorHandler, numShards int, batchSize int, capacity int, pusher Pusher) *parallelStorageShards {
	p := &parallelStorageShards{
		numShards:    numShards,
		pusher:       pusher,
		errorHandler: errorHandler,
		capacity:     capacity,
		metrics:      metrics,
		batchSize:    batchSize,
		wg:           &sync.WaitGroup{},
	}

	p.start()

	return p
}

// Compute a hash from LabelAdapters, avoiding the cost of conversion to Labels.
// There is no particular benefit to match the hash function used by TSDB;
// its main stripes are split by unique ID which we don't yet know.
func labelAdaptersHash(b []byte, ls []mimirpb.LabelAdapter) ([]byte, uint64) {
	const sep = '\xff'
	b = b[:0]
	for _, v := range ls {
		b = append(b, v.Name...)
		b = append(b, sep)
		b = append(b, v.Value...)
		b = append(b, sep)
	}
	return b, xxhash.Sum64(b)
}

// PushToStorage hashes each time series in the write requests and sends them to the appropriate shard which is then handled by the current batchingQueue in that shard.
// PushToStorage ignores SkipLabelNameValidation because that field is only used in the distributor and not in the ingester.
// PushToStorage aborts the request if it encounters an error.
func (p *parallelStorageShards) PushToStorage(ctx context.Context, request *mimirpb.WriteRequest) error {
	hashBuf := make([]byte, 0, 1024)
	for _, ts := range request.Timeseries {
		var shard uint64
		hashBuf, shard = labelAdaptersHash(hashBuf, ts.Labels)
		shard = shard % uint64(p.numShards)

		if err := p.shards[shard].AddToBatch(ctx, request.Source, ts); err != nil {
			return fmt.Errorf("encountered a non-client error when ingesting; this error was for a previous write request for the same tenant: %w", err)
		}
	}

	// Push metadata to every shard in a round-robin fashion.
	// Start from a random shard to avoid hotspots in the first few shards when there are not many metadata pieces in each request.
	shard := rand.IntN(p.numShards)
	for mdIdx := range request.Metadata {
		if err := p.shards[shard].AddMetadataToBatch(ctx, request.Source, request.Metadata[mdIdx]); err != nil {
			return fmt.Errorf("encountered a non-client error when ingesting; this error was for a previous write request for the same tenant: %w", err)
		}
		shard++
		shard %= p.numShards
	}

	// We might have some data left in some of the queues in the shards, but they will be flushed eventually once Stop is called, and we're certain that no more data is coming.
	// So far we didn't find any non-client errors that are worth aborting for.
	// We'll call Close eventually and collect the rest.
	return nil
}

// Close stops all the shards and waits for them to finish.
func (p *parallelStorageShards) Close() []error {
	var errs multierror.MultiError

	for _, shard := range p.shards {
		errs.Add(shard.Close())
	}

	p.wg.Wait()

	return errs
}

// start starts the shards, each in its own goroutine.
func (p *parallelStorageShards) start() {
	shards := make([]*batchingQueue, p.numShards)
	p.wg.Add(p.numShards)

	for i := range shards {
		shards[i] = newBatchingQueue(p.capacity, p.batchSize, p.metrics.batchingQueueMetrics)
		go p.run(shards[i])
	}

	p.shards = shards
}

// run runs the batchingQueue for the shard.
func (p *parallelStorageShards) run(queue *batchingQueue) {
	defer p.wg.Done()
	defer queue.Done()

	// By design of the queue, we must drain the queue, otherwise a deadlock could happen.
	for wr := range queue.Channel() {
		p.metrics.batchAge.Observe(time.Since(wr.startedAt).Seconds())
		p.metrics.timeSeriesPerFlush.Observe(float64(len(wr.Timeseries)))
		processingStart := time.Now()

		err := p.pusher.PushToStorage(wr.Context, wr.WriteRequest)

		// The error handler needs to determine if this is a server error or not.
		// If it is, we need to stop processing as the batch will be retried. When is not (client error), it'll log it, and we can continue processing.
		p.metrics.processingTime.WithLabelValues(requestContents(wr.WriteRequest)).Observe(time.Since(processingStart).Seconds())
		if p.errorHandler.IsServerError(wr.Context, err) {
			queue.ReportError(err)
		}
	}
}

func requestContents(request *mimirpb.WriteRequest) string {
	switch {
	case len(request.Timeseries) > 0 && len(request.Metadata) > 0:
		return "timeseries_and_metadata"
	case len(request.Timeseries) > 0:
		return "timeseries"
	case len(request.Metadata) > 0:
		return "metadata"
	default:
		// This would be a bug, but at least we'd know.
		return "empty"
	}
}

// pushErrorHandler filters out client errors and logs them.
// It only returns errors that are not client errors.
type pushErrorHandler struct {
	metrics          *storagePusherMetrics
	clientErrSampler *util_log.Sampler
	fallbackLogger   log.Logger
}

// newPushErrorHandler creates a new pushErrorHandler instance.
func newPushErrorHandler(metrics *storagePusherMetrics, clientErrSampler *util_log.Sampler, fallbackLogger log.Logger) *pushErrorHandler {
	return &pushErrorHandler{
		metrics:          metrics,
		clientErrSampler: clientErrSampler,
		fallbackLogger:   fallbackLogger,
	}
}

// IsServerError returns whether the error is a server error or not, the context is used to extract the span from the trace.
// When the error is a server error, we'll add it to the span passed down in the context and return true to indicate that the we should stop processing.
// When it is a client error, we'll add it to the span and log it to stdout/stderr.
func (p *pushErrorHandler) IsServerError(ctx context.Context, err error) bool {
	// For every request, we have to determine if it's a server error.
	// For the sake of simplicity, let's increment the total requests counter here.
	p.metrics.totalRequests.Inc()

	if err == nil {
		return false
	}
	spanLog := spanlogger.FromContext(ctx, p.fallbackLogger)

	// Only return non-client errors; these will stop the processing of the current Kafka fetches and retry (possibly).
	if !mimirpb.IsClientError(err) {
		p.metrics.serverErrRequests.Inc()
		_ = spanLog.Error(err)
		return true
	}

	p.metrics.clientErrRequests.Inc()

	// The error could be sampled or marked to be skipped in logs, so we check whether it should be
	// logged before doing it.
	if keep, reason := p.shouldLogClientError(ctx, err); keep {
		if reason != "" {
			err = fmt.Errorf("%w (%s)", err, reason)
		}

		// This error message is consistent with error message in Prometheus remote-write and OTLP handlers in distributors.
		level.Warn(spanLog).Log("msg", "detected a client error while ingesting write request (the request may have been partially ingested)", "insight", true, "err", err)
	}
	return false
}

// shouldLogClientError returns whether err should be logged.
func (p *pushErrorHandler) shouldLogClientError(ctx context.Context, err error) (bool, string) {
	var optional middleware.OptionalLogging
	if !errors.As(err, &optional) {
		// If error isn't sampled yet, we wrap it into our sampler and try again.
		err = p.clientErrSampler.WrapError(err)
		if !errors.As(err, &optional) {
			// We can get here if c.clientErrSampler is nil.
			return true, ""
		}
	}

	return optional.ShouldLog(ctx)
}

// batchingQueue is a queue that batches the incoming time series according to the batch size.
// Once the batch size is reached, the batch is pushed to a channel which can be accessed through the Channel() method.
//
// Contract:
// - The queue must always be drained by the consumer.
type batchingQueue struct {
	metrics *batchingQueueMetrics

	ch chan flushableWriteRequest

	// errs is the list of errors reported by the queue consumer. We don't use a buffered channel
	// so that we don't have to reason about the required capacity to avoid any deadlock between
	// producer (that collect errors) and consumer (that can report errors). The concurrency around
	// this queue is tricky.
	errs   multierror.MultiError
	errsMx sync.Mutex

	// done channel gets closed once there's no more data that will be enqueued.
	done chan struct{}

	currentBatch flushableWriteRequest
	batchSize    int
}

// newBatchingQueue creates a new batchingQueue instance.
func newBatchingQueue(capacity int, batchSize int, metrics *batchingQueueMetrics) *batchingQueue {
	return &batchingQueue{
		metrics:      metrics,
		ch:           make(chan flushableWriteRequest, capacity),
		done:         make(chan struct{}),
		currentBatch: flushableWriteRequest{WriteRequest: &mimirpb.WriteRequest{Timeseries: mimirpb.PreallocTimeseriesSliceFromPool()}},
		batchSize:    batchSize,
	}
}

// AddToBatch adds a time series to the current batch. If the batch size is reached, the batch is pushed to the Channel().
// If an error occurs while pushing the batch, it returns the error and ensures the batch is pushed.
func (q *batchingQueue) AddToBatch(ctx context.Context, source mimirpb.WriteRequest_SourceEnum, ts mimirpb.PreallocTimeseries) error {
	if q.currentBatch.startedAt.IsZero() {
		q.currentBatch.startedAt = time.Now()
	}
	q.currentBatch.Timeseries = append(q.currentBatch.Timeseries, ts)
	q.currentBatch.Context = ctx
	q.currentBatch.Source = source

	return q.pushIfFull()
}

// AddMetadataToBatch adds metadata to the current batch.
func (q *batchingQueue) AddMetadataToBatch(ctx context.Context, source mimirpb.WriteRequest_SourceEnum, metadata *mimirpb.MetricMetadata) error {
	if q.currentBatch.startedAt.IsZero() {
		q.currentBatch.startedAt = time.Now()
	}
	q.currentBatch.Metadata = append(q.currentBatch.Metadata, metadata)
	q.currentBatch.Context = ctx
	q.currentBatch.Source = source

	return q.pushIfFull()
}

// Close closes the batchingQueue, it'll push the current branch to the channel if it's not empty.
// and then close the channel.
func (q *batchingQueue) Close() error {
	var errs multierror.MultiError
	if len(q.currentBatch.Timeseries)+len(q.currentBatch.Metadata) > 0 {
		if err := q.push(); err != nil {
			errs.Add(err)
		}
	}

	close(q.ch)
	<-q.done

	errs = append(errs, q.collectErrors()...)
	return errs.Err()
}

// Channel returns the channel where the batches are pushed.
func (q *batchingQueue) Channel() <-chan flushableWriteRequest {
	return q.ch
}

// ReportError reports an error occurred processing a flushableWriteRequest consumed from the queue.
func (q *batchingQueue) ReportError(err error) {
	if err == nil {
		return
	}

	q.errsMx.Lock()
	q.errs.Add(err)
	q.errsMx.Unlock()
}

// Done signals the queue that there is no more data coming for the channel, and no more error reported via ReportError().
// It is necessary to ensure we don't close the channel before all the data is flushed.
func (q *batchingQueue) Done() {
	close(q.done)
}

func (q *batchingQueue) pushIfFull() error {
	if len(q.currentBatch.Metadata)+len(q.currentBatch.Timeseries) >= q.batchSize {
		return q.push()
	}
	return nil
}

// push pushes the current batch to the channel and resets the current batch.
// It also collects any errors that might have occurred while processing any previous batch.
func (q *batchingQueue) push() error {
	errs := q.collectErrors()

	q.metrics.flushErrorsTotal.Add(float64(len(errs)))
	q.metrics.flushTotal.Inc()

	// By design, we expect the queue to always be drained by whoever uses it. So we don't worry
	// whether this call could block *forever*. If it does, then it's a bug.
	q.ch <- q.currentBatch
	q.resetCurrentBatch()

	return errs.Err()
}

// resetCurrentBatch resets the current batch to an empty state.
func (q *batchingQueue) resetCurrentBatch() {
	q.currentBatch = flushableWriteRequest{
		WriteRequest: &mimirpb.WriteRequest{Timeseries: mimirpb.PreallocTimeseriesSliceFromPool()},
	}
}

func (q *batchingQueue) collectErrors() multierror.MultiError {
	var returnErrs multierror.MultiError

	q.errsMx.Lock()
	if len(q.errs) > 0 {
		returnErrs = q.errs
		q.errs = multierror.MultiError{}
	}
	q.errsMx.Unlock()

	return returnErrs
}
