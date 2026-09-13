package main

import (
	pb "github.com/OwnSecurityGuard/gametrace/sdk/proto"

	"github.com/OwnSecurityGuard/gametrace/sdk/event"
)

// emit turns one parsed HTTP message into a schema-conformant event carrying
// the declared state-change entry, then sends it.
func (d *decoder) emit(stream pb.Decoder_DecodeV2Server, inputID, flowID string, m *httpMessage) error {
	c := d.counts[flowID]
	var draft event.Draft
	if m.isRequest {
		c.requests++
		draft = event.Draft{
			Type: "http.request",
			Value: event.ValueFromMap(map[string]any{
				"flow_id":        flowID,
				"requests":       c.requests,
				"method":         m.method,
				"path":           m.path,
				"host":           m.host,
				"content_length": m.contentLength,
				"_state_changes": []any{
					map[string]any{
						"subject_type": "flow",
						"subject_id":   flowID,
						"op":           "set",
						"path":         "requests",
						"before":       c.requests - 1,
						"after":        c.requests,
						"version":      c.requests + c.responses,
					},
				},
			}),
			CorrelationKey: flowID,
		}
	} else {
		c.responses++
		draft = event.Draft{
			Type: "http.response",
			Value: event.ValueFromMap(map[string]any{
				"flow_id":        flowID,
				"responses":      c.responses,
				"status":         m.status,
				"content_length": m.contentLength,
				"_state_changes": []any{
					map[string]any{
						"subject_type": "flow",
						"subject_id":   flowID,
						"op":           "set",
						"path":         "responses",
						"before":       c.responses - 1,
						"after":        c.responses,
						"version":      c.requests + c.responses,
					},
				},
			}),
			CorrelationKey: flowID,
		}
	}
	d.counts[flowID] = c

	resp, err := draft.ToResponse(inputID)
	if err != nil {
		return stream.Send(&pb.DecodeResponseV2{InputId: inputID, Done: true, Error: err.Error()})
	}
	return stream.Send(resp)
}
