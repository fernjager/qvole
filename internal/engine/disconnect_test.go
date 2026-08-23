package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/quic-go/quic-go"
)

func TestIsPeerDisconnect(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"local cancel", context.Canceled, false},
		{"plain error", errors.New("boom"), false},
		{"fmt wrapped plain error", fmt.Errorf("wrapped: %w", errors.New("boom")), false},
		{"application error", &quic.ApplicationError{Remote: true, ErrorCode: 1, ErrorMessage: "canceled"}, true},
		{"application error code 0", &quic.ApplicationError{Remote: true, ErrorCode: 0}, true},
		{"wrapped application error", fmt.Errorf("accept: %w", &quic.ApplicationError{Remote: true, ErrorCode: 1}), true},
		{"idle timeout", &quic.IdleTimeoutError{}, true},
		{"stateless reset", &quic.StatelessResetError{}, true},
		{"transport error", &quic.TransportError{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPeerDisconnect(tc.err); got != tc.want {
				t.Errorf("IsPeerDisconnect(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
