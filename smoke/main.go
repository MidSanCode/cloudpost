// Command smoke is a throwaway end-to-end test client for cloudpost.
package main

import (
	"bytes"
	"fmt"
	"net"
	"net/smtp"
	"time"
)

func main() {
	// 1. SMTP submit: auth + send to alice@test.local
	c, err := smtp.Dial("127.0.0.1:2525")
	if err != nil {
		fmt.Println("DIAL ERR:", err)
		return
	}
	defer c.Quit()
	c.Hello("client.test")
	if err := c.Auth(smtp.PlainAuth("", "alice@test.local", "alicepw1", "127.0.0.1")); err != nil {
		fmt.Println("AUTH ERR:", err)
		return
	}
	if err := c.Mail("alice@test.local"); err != nil {
		fmt.Println("MAIL ERR:", err)
		return
	}
	if err := c.Rcpt("alice@test.local"); err != nil {
		fmt.Println("RCPT ERR:", err)
		return
	}
	w, err := c.Data()
	if err != nil {
		fmt.Println("DATA ERR:", err)
		return
	}
	msg := "From: alice@test.local\r\nTo: alice@test.local\r\nSubject: =?utf-8?B?5L2g5aW9IPCfkKbvuI8=?=\r\nDate: Mon, 06 Jan 2025 08:00:00 +0000\r\nMessage-ID: <t1@test.local>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHello World - smoke test body"
	w.Write([]byte(msg))
	w.Close()
	fmt.Println("SMTP deliver OK")

	// 2. POP3: USER/PASS/STAT/UIDL/RETR
	pc, err := net.Dial("tcp", "127.0.0.1:1110")
	if err != nil {
		fmt.Println("POP DIAL ERR:", err)
		return
	}
	tmp := make([]byte, 65536)
	readResp := func() string {
		pc.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := pc.Read(tmp)
		return string(tmp[:n])
	}
	fmt.Print("POP greet: ", readResp())
	fmt.Fprint(pc, "USER alice@test.local\r\n")
	fmt.Print("USER: ", readResp())
	fmt.Fprint(pc, "PASS alicepw1\r\n")
	fmt.Print("PASS: ", readResp())
	fmt.Fprint(pc, "STAT\r\n")
	fmt.Print("STAT: ", readResp())
	fmt.Fprint(pc, "LIST\r\n")
	time.Sleep(150 * time.Millisecond)
	fmt.Print("LIST: ", readResp())
	fmt.Fprint(pc, "RETR 1\r\n")
	time.Sleep(200 * time.Millisecond)
	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	var retr bytes.Buffer
	for {
		n, err := pc.Read(tmp)
		retr.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	fmt.Println("RETR has-subject:", bytes.Contains(retr.Bytes(), []byte("Subject:")), "| has-body:", bytes.Contains(retr.Bytes(), []byte("Hello World")))
	fmt.Fprint(pc, "QUIT\r\n")
	pc.Close()

	// 3. IMAP: login/select/fetch/search
	ic, err := net.Dial("tcp", "127.0.0.1:1143")
	if err != nil {
		fmt.Println("IMAP DIAL ERR:", err)
		return
	}
	readI := func() string {
		ic.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := ic.Read(tmp)
		return string(tmp[:n])
	}
	fmt.Print("IMAP greet: ", readI())
	fmt.Fprint(ic, "a1 LOGIN alice@test.local alicepw1\r\n")
	fmt.Print("LOGIN: ", readI())
	fmt.Fprint(ic, "a2 SELECT INBOX\r\n")
	fmt.Print("SELECT: ", readI())
	fmt.Fprint(ic, "a3 FETCH 1 (UID FLAGS RFC822.SIZE)\r\n")
	fmt.Print("FETCH: ", readI())
	fmt.Fprint(ic, "a4 SEARCH SUBJECT \"smoke\"\r\n")
	fmt.Print("SEARCH: ", readI())
	fmt.Fprint(ic, "a5 LOGOUT\r\n")
	time.Sleep(200 * time.Millisecond)
	ic.Close()
}
