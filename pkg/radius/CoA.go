package radius

import (
	"errors"
	"github.com/blind-oracle/go-radius"
	"net"
	"time"
)

// Encapsulates all data needed for a RADIUS CoA request
type CoARequest struct {
	SessionUid       string
	SessionStartDate time.Time
	SwitchIP         net.IP
	SwitchSecret     []byte
}

// Send off the request to the switch. This uses a cisco-specific extension to toggle the port.
func (c *CoARequest) SendDisconnect() error {
	client := radius.Client{
		Retries: 2,
		Timeout: 200*time.Millisecond,
	}
	request := radius.New(radius.CodeCoARequest, c.SwitchSecret)
	err := request.Add("Acct-Session-Id", c.SessionUid)
	if err != nil {
		return err
	}
	err = request.Dictionary.Register("Event-Timestamp", 55, radius.AttributeTime)
	if err != nil {
		return err
	}
	// Mon Jan 2 15:04:05 -0700 MST 2006
	err = request.Add("Event-Timestamp", c.SessionStartDate)
	if err != nil {
		return err
	}
	err = request.Add("Vendor-Specific", radius.EncodeAVPairCisco("subscriber:command=bounce-host-port"))
	if err != nil {
		return err
	}
	err = request.Add("NAS-IP-Address", c.SwitchIP)
	if err != nil {
		return err
	}

	dest := net.UDPAddr{
		IP: c.SwitchIP,
		Port: 3799,
	}
	result, err := client.Exchange(request, &dest, nil)
	if err != nil {
		return err
	}
	if result != nil {
		if result.Code != radius.CodeCoAACK {
			return errors.New("NACK from switch")
		}
	}
	return nil
}
