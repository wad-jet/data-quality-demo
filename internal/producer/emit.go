package producer

import (
	"context"
	"errors"
	"fmt"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"strings"
	"sync"
	"sync/atomic"
)

type Emitter struct {
	failures  int64
	bootstrap []string
	topic     string
	clientMu  sync.Mutex
	client    *kgo.Client
	// placeholder fields for compatibility
	_unused sync.Mutex
}

func NewEmitter(bootstrap []string, topic string) *Emitter {
	return &Emitter{bootstrap: bootstrap, topic: topic}
}

func (e *Emitter) initClient() {
	e.clientMu.Lock()
	defer e.clientMu.Unlock()
	if e.client != nil {
		return
	}
	var err error
	e.client, err = kgo.NewClient(
		kgo.SeedBrokers(e.bootstrap...),
		kgo.DefaultProduceTopic(e.topic),
	)
	if err != nil {
		// In lazy init we cannot return error; store nil client and let Send handle it.
		// For simplicity, panic as this indicates misconfiguration.
		panic(err)
	}
}

func (e *Emitter) EnsureTopic(ctx context.Context) error {
	client, err := kgo.NewClient(kgo.SeedBrokers(e.bootstrap...))
	if err != nil {
		return err
	}
	defer client.Close()
	req := kmsg.NewPtrCreateTopicsRequest()
	rt := kmsg.NewCreateTopicsRequestTopic()
	rt.Topic = e.topic
	rt.NumPartitions = 1
	rt.ReplicationFactor = 1
	req.Topics = append(req.Topics, rt)
	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		return err
	}
	if len(resp.Topics) == 0 {
		return fmt.Errorf("create topics: empty response for %s", e.topic)
	}
	code := resp.Topics[0].ErrorCode
	if code == 0 {
		return nil
	}
	if err2 := kerr.ErrorForCode(code); err2 != nil && errors.Is(err2, kerr.TopicAlreadyExists) {
		return nil
	}
	return fmt.Errorf("create topic %s: %v", e.topic, kerr.ErrorForCode(code))
}

func (e *Emitter) Send(ctx context.Context, key, payload []byte) error {
	e.initClient()
	var prodErr error
	e.client.Produce(ctx, &kgo.Record{Topic: e.topic, Key: key, Value: payload}, func(r *kgo.Record, err error) {
		if err != nil {
			atomic.AddInt64(&e.failures, 1)
			prodErr = err
		}
	})
	// Flush to ensure delivery before returning.
	_ = e.client.Flush(ctx)
	if prodErr != nil && strings.Contains(prodErr.Error(), "UNKNOWN_TOPIC_OR_PARTITION") {
		// attempt to create topic and retry once
		_ = e.EnsureTopic(ctx)
		// retry produce
		prodErr = nil
		e.client.Produce(ctx, &kgo.Record{Topic: e.topic, Key: key, Value: payload}, func(r *kgo.Record, err error) {
			if err != nil {
				atomic.AddInt64(&e.failures, 1)
				prodErr = err
			}
		})
		_ = e.client.Flush(ctx)
	}
	return prodErr
}

func (e *Emitter) SendWithHeaders(ctx context.Context, key, payload []byte, headers []kgo.RecordHeader) error {
	e.initClient()
	var prodErr error
	e.client.Produce(ctx, &kgo.Record{Topic: e.topic, Key: key, Value: payload, Headers: headers}, func(r *kgo.Record, err error) {
		if err != nil {
			atomic.AddInt64(&e.failures, 1)
			prodErr = err
		}
	})
	_ = e.client.Flush(ctx)
	return prodErr
}

func (e *Emitter) Failures() int64 { return atomic.LoadInt64(&e.failures) }

func (e *Emitter) Close() error {
	if e.client != nil {
		e.client.Close()
	}
	return nil
}
