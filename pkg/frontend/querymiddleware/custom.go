// SPDX-License-Identifier: AGPL-3.0-only

package querymiddleware

import (
	"github.com/grafana/mimir/pkg/mimirpb"
)

var _ mimirpb.BufferHolder = &PrometheusResponse{}
