package kafkax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// TopicSettings returns the replication factor and min.insync.replicas for a
// cluster of n brokers: RF = min(3, n), min ISR = RF-1 (but at least 1).
// With RF=3 and min ISR=2 an acks=all write survives one broker down;
// on a single-node dev cluster both are 1.
func TopicSettings(brokers int) (replicationFactor int16, minISR int) {
	rf := min(3, max(brokers, 1))
	return int16(rf), max(1, rf-1)
}

// TopicConfigs are the topic-level configs from CONTRACT.md.
func TopicConfigs(minISR int) map[string]*string {
	return map[string]*string{
		"retention.ms":        kadm.StringPtr(strconv.FormatInt(TopicRetention.Milliseconds(), 10)),
		"min.insync.replicas": kadm.StringPtr(strconv.Itoa(minISR)),
	}
}

// EnsureTopic creates the topic if it does not exist (KAFKA_AUTO_CREATE_TOPIC=true).
// An existing topic is left as is: its partitions and configs belong to
// whoever created it (in Kubernetes — a Strimzi KafkaTopic).
func EnsureTopic(ctx context.Context, cl *kgo.Client, topic string, log *slog.Logger) error {
	adm := kadm.NewClient(cl) // shares cl; do not Close adm, it would close cl
	brokers, err := adm.ListBrokers(ctx)
	if err != nil {
		return fmt.Errorf("list brokers: %w", err)
	}
	rf, minISR := TopicSettings(len(brokers))
	_, err = adm.CreateTopic(ctx, TopicPartitions, rf, TopicConfigs(minISR), topic)
	switch {
	case errors.Is(err, kerr.TopicAlreadyExists):
		log.Info("kafka topic exists", "topic", topic)
		return nil
	case err != nil:
		return fmt.Errorf("create topic %s: %w", topic, err)
	}
	log.Info("kafka topic created", "topic", topic, "partitions", TopicPartitions,
		"replication_factor", rf, "min_insync_replicas", minISR, "brokers", len(brokers))
	return nil
}
