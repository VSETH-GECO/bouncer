package radius

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/blind-oracle/go-radius"
)

// CoARequest encapsulates all data needed for a RADIUS CoA request
type CoARequest struct {
	SessionUID       string
	SessionStartDate time.Time
	SwitchIP         net.IP
	SwitchSecret     []byte
}

// SendDisconnect sends off the request to the switch. This uses a cisco-specific extension to toggle the port.
//
// The underlying RADIUS client does not support contexts. ctx is checked before the request is sent, and its deadline
// (if any) caps the per-attempt timeout; once sent, the exchange runs to completion, which is bounded by the client's
// timeout and retry count (well below a second).
func (c *CoARequest) SendDisconnect(ctx context.Context) error {
	client := radius.Client{
		Retries: 2,
		Timeout: 200 * time.Millisecond,
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < client.Timeout {
			client.Timeout = remaining
		}
	}

	request := radius.New(radius.CodeCoARequest, c.SwitchSecret)
	err := request.Add("Acct-Session-Id", c.SessionUID)
	if err != nil {
		return fmt.Errorf("adding Acct-Session-Id: %w", err)
	}
	if _, ok := request.Dictionary.Type("Event-Timestamp"); !ok {
		err = request.Dictionary.Register("Event-Timestamp", 55, radius.AttributeTime)
		if err != nil {
			return fmt.Errorf("registering Event-Timestamp attribute: %w", err)
		}
	}
	// Mon Jan 2 15:04:05 -0700 MST 2006
	err = request.Add("Event-Timestamp", c.SessionStartDate)
	if err != nil {
		return fmt.Errorf("adding Event-Timestamp: %w", err)
	}
	err = request.Add("Vendor-Specific", radius.EncodeAVPairCisco("subscriber:command=bounce-host-port"))
	if err != nil {
		return fmt.Errorf("adding Vendor-Specific: %w", err)
	}
	err = request.Add("NAS-IP-Address", c.SwitchIP)
	if err != nil {
		return fmt.Errorf("adding NAS-IP-Address: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	dest := net.UDPAddr{
		IP:   c.SwitchIP,
		Port: 3799,
	}
	result, err := client.Exchange(request, &dest, nil)
	if err != nil {
		return fmt.Errorf("exchanging CoA with %s: %w", dest.String(), err)
	}
	if result != nil {
		if result.Code != radius.CodeCoAACK {
			return fmt.Errorf("NACK from switch %s (code %d)", c.SwitchIP, result.Code)
		}
	}
	return nil
}
