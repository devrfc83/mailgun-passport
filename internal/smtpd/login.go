package smtpd

import "github.com/emersion/go-sasl"

// loginServer is the server side of the obsolete but common LOGIN mechanism,
// which go-sasl only implements for clients. Outlook, older PHP mailers and
// many appliances offer nothing else. The client may send the username as
// the initial response (AUTH LOGIN <base64>).
type loginServer struct {
	step         int
	username     string
	authenticate func(username, password string) error
}

func (s *loginServer) Next(response []byte) ([]byte, bool, error) {
	switch {
	case s.step == 0 && response == nil:
		s.step = 1
		return []byte("Username:"), false, nil
	case s.step <= 1:
		s.username = string(response)
		s.step = 2
		return []byte("Password:"), false, nil
	case s.step == 2:
		s.step = 3
		return nil, true, s.authenticate(s.username, string(response))
	}
	return nil, false, sasl.ErrUnexpectedClientResponse
}
