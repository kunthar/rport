// Package responsible for sharing logic to handle communication between a server and clients.
package comm

import (
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/ssh"

	chshare "github.com/cloudradar-monitoring/rport/share"
)

// ReplyError sends a failure response with a given error message if not nil to a given request.
func ReplyError(log *chshare.Logger, req *ssh.Request, err error) {
	var errMsg string
	if err != nil {
		errMsg = err.Error()
	}

	if replyErr := req.Reply(false, []byte(errMsg)); replyErr != nil {
		log.Errorf("Failed to reply an error response: %v", replyErr)
	}
}

// ReplySuccessJSON sends a success response with a given value as JSON to a given request.
// Response expected to be a value that can be encoded into JSON, otherwise - a failure will be replied.
func ReplySuccessJSON(log *chshare.Logger, req *ssh.Request, resp interface{}) {
	respBytes, err := json.Marshal(resp)
	if err != nil {
		log.Errorf("Failed to encode success response %T: %v", resp, err)
		ReplyError(log, req, err)
		return
	}

	if err = req.Reply(true, respBytes); err != nil {
		log.Errorf("Failed to reply a success response %T: %v", resp, err)
	}
}

// SendRequestAndGetResponse sends a given request, parses a returned response and stores a success result in a given destination value.
// Returns an error on a failure response or if an error happen. Error will be ClientError type if the error is a client error.
// Both request and response are expected to be JSON.
func SendRequestAndGetResponse(conn ssh.Conn, reqType string, req, successRespDest interface{}) error {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to encode request %T: %v", req, err)
	}

	ok, respBytes, err := conn.SendRequest(reqType, true, reqBytes)
	if err != nil {
		return fmt.Errorf("failed to send request: %v", err)
	}

	if !ok {
		return NewClientError(fmt.Errorf("client error: %s", respBytes))
	}

	if successRespDest != nil {
		if err := json.Unmarshal(respBytes, successRespDest); err != nil {
			return NewClientError(fmt.Errorf("invalid client response format: failed to decode response into %T: %v", successRespDest, err))
		}
	}

	return nil
}

type ClientError struct {
	err error
}

func NewClientError(err error) *ClientError {
	return &ClientError{err: err}
}

func (e *ClientError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}
