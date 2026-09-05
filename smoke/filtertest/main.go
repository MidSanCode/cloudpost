package main

import (
	"fmt"
	"net/smtp"
)

func main() {
	c, err := smtp.Dial("127.0.0.1:2525")
	if err != nil {
		fmt.Println("DIAL:", err)
		return
	}
	defer c.Quit()
	c.Hello("t")
	c.Auth(smtp.PlainAuth("", "alice@test.local", "alicepw1", "127.0.0.1"))
	c.Mail("newsletter@vendor.com")
	c.Rcpt("alice@test.local")
	w, _ := c.Data()
	w.Write([]byte("From: Big Vendor <newsletter@vendor.com>\r\nTo: alice@test.local\r\nSubject: Weekly deals\r\nMessage-ID: <n1@vendor.com>\r\n\r\nnewsletter body"))
	w.Close()
	fmt.Println("sent")
}
