package fetch

import (
	"fmt"

	"cloudpost/internal/mailstore"
)

// TestResult reports a remote connection probe.
type TestResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Folder string `json:"folder,omitempty"`
}

// TestConnection probes remote credentials without changing state.
func (f *Fetcher) TestConnection(a *mailstore.Account) TestResult {
	switch a.RemoteProto {
	case "imap":
		return f.testIMAP(a)
	case "pop3":
		return f.testPOP3(a)
	}
	return TestResult{OK: false, Detail: "proto must be imap or pop3"}
}

func (f *Fetcher) testIMAP(a *mailstore.Account) TestResult {
	c, err := dialIMAP(a)
	if err != nil {
		return TestResult{OK: false, Detail: err.Error()}
	}
	defer c.Logout().Wait()
	if err := c.Login(a.RemoteUser, a.RemotePass).Wait(); err != nil {
		return TestResult{OK: false, Detail: "login failed: " + trimErr(err)}
	}
	sel, err := c.Select(a.RemoteFolder, readOnlySelect).Wait()
	if err != nil {
		return TestResult{OK: true, Detail: fmt.Sprintf("login ok, but cannot open folder %s", a.RemoteFolder), Folder: a.RemoteFolder}
	}
	return TestResult{OK: true, Detail: fmt.Sprintf("ok: %d messages in %s", sel.NumMessages, a.RemoteFolder), Folder: a.RemoteFolder}
}

func (f *Fetcher) testPOP3(a *mailstore.Account) TestResult {
	c, err := pop3Connect(a.RemoteHost, a.RemotePort, a.RemoteTLS)
	if err != nil {
		return TestResult{OK: false, Detail: err.Error()}
	}
	defer c.close()
	if err := c.login(a.RemoteUser, a.RemotePass); err != nil {
		return TestResult{OK: false, Detail: "login failed: " + trimErr(err)}
	}
	if err := c.cmd("STAT"); err != nil {
		return TestResult{OK: false, Detail: "stat failed: " + trimErr(err)}
	}
	return TestResult{OK: true, Detail: "ok: login and STAT succeeded"}
}
