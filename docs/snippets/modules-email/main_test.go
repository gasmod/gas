package main

import (
	"context"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/email/emailtest"
)

// #region testing
func TestWelcomeSendsOneTemplatedEmail(t *testing.T) {
	mock := &emailtest.MockEmail{}
	orders := NewOrders(mock)

	if err := orders.Welcome(context.Background(), "ada@example.com", "Ada"); err != nil {
		t.Fatal(err)
	}

	if n := mock.CallCount("SendFromTemplate"); n != 1 {
		t.Fatalf("SendFromTemplate called %d times, want 1", n)
	}
	msg := mock.Calls[0].Args[0].(*gas.TemplatedEmail)
	if msg.To[0] != "ada@example.com" {
		t.Fatalf("sent to %v", msg.To)
	}
}

// #endregion testing
