// SPDX-License-Identifier: AGPL-3.0-only

package ruler

import (
	"github.com/grafana/mimir/pkg/mimirpb"
)

var _ mimirpb.BufferHolder = &RulesResponse{}
