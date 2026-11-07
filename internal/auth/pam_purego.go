package auth

import "github.com/stubbedev/lys-lib/pam"

func CheckLibrary() error { return pam.CheckLibrary() }

type systemPAM struct {
	confdir string
}

func (s systemPAM) start(service, user string, converse func([]pamMessage) ([]pamReply, int32)) (pamTxn, error) {
	txn, err := pam.Start(service, user, s.confdir, func(ms []pam.Message) ([]pam.Reply, int32) {
		msgs := make([]pamMessage, len(ms))
		for i, m := range ms {
			msgs[i] = pamMessage{style: int32(m.Style), text: m.Text}
		}
		replies, code := converse(msgs)
		out := make([]pam.Reply, len(replies))
		for i, r := range replies {
			out[i] = pam.Reply{Text: r.text, Set: r.set}
		}
		return out, code
	})
	if err != nil {
		return nil, err
	}
	return systemTxn{txn}, nil
}

type systemTxn struct{ t *pam.Txn }

func (s systemTxn) authenticate() int32         { return s.t.Authenticate() }
func (s systemTxn) acctMgmt() int32             { return s.t.AcctMgmt() }
func (s systemTxn) chauthtok(flags int32) int32 { return s.t.Chauthtok(flags) }
func (s systemTxn) setcred(flags int32) int32   { return s.t.Setcred(flags) }
func (s systemTxn) openSession() int32          { return s.t.OpenSession() }
func (s systemTxn) closeSession() int32         { return s.t.CloseSession() }
func (s systemTxn) putenv(kv string) int32      { return s.t.Putenv(kv) }
func (s systemTxn) getenvlist() []string        { return s.t.Getenvlist() }
func (s systemTxn) end(status int32)            { s.t.End(status) }
func (s systemTxn) strerror(code int32) string  { return s.t.Strerror(code) }
