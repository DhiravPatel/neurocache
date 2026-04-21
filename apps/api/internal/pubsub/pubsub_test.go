package pubsub

import (
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	b := New(16)
	sub := b.Subscribe("events")
	defer sub.Close()

	n := b.Publish("events", "hello")
	if n != 1 {
		t.Fatalf("Publish delivered to %d subs, want 1", n)
	}
	select {
	case msg := <-sub.Ch():
		if msg.Channel != "events" || msg.Payload != "hello" {
			t.Errorf("got %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("no message delivered")
	}
}

func TestPatternMatching(t *testing.T) {
	b := New(16)
	p := b.PSubscribe("user:*")
	defer p.Close()

	b.Publish("user:42", "hi")
	b.Publish("admin:42", "nope")

	select {
	case msg := <-p.Ch():
		if msg.Pattern != "user:*" || msg.Channel != "user:42" {
			t.Errorf("got %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("pattern subscriber missed message")
	}

	// admin:42 should not land on this subscriber
	select {
	case msg := <-p.Ch():
		t.Errorf("unexpected message: %+v", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnsubscribeCleans(t *testing.T) {
	b := New(16)
	sub := b.Subscribe("x")
	b.Publish("x", "1")
	<-sub.Ch()
	sub.Close()
	n := b.Publish("x", "2")
	if n != 0 {
		t.Errorf("Publish after close delivered to %d", n)
	}
	if len(b.Channels("*")) != 0 {
		t.Errorf("channels remain: %v", b.Channels("*"))
	}
}

func TestNumSubAndNumPat(t *testing.T) {
	b := New(16)
	a := b.Subscribe("a")
	defer a.Close()
	a2 := b.Subscribe("a")
	defer a2.Close()
	p := b.PSubscribe("x*", "y*")
	defer p.Close()

	counts := b.NumSub("a", "b")
	if counts["a"] != 2 || counts["b"] != 0 {
		t.Errorf("NumSub = %v", counts)
	}
	if n := b.NumPat(); n != 2 {
		t.Errorf("NumPat = %d", n)
	}
}
