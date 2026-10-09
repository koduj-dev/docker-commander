package history

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisStore keeps each (container, metric) series in a Redis sorted set scored
// by timestamp. Members are "<ts>:<value>" so equal values at different times
// stay distinct. Old points are trimmed on write and keys carry a TTL.
type redisStore struct {
	rdb       *redis.Client
	retention time.Duration
}

func newRedisStore(ctx context.Context, cfg Config) (*redisStore, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := rdb.Ping(pctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return &redisStore{rdb: rdb, retention: cfg.Retention}, nil
}

func key(containerID, metric string) string {
	return "dc:m:" + containerID + ":" + metric
}

// hostKey holds which host a container's samples came from, so a series can be
// authorised. It carries the same TTL as the series it describes.
func hostKey(containerID string) string {
	return "dc:mh:" + containerID
}

func (s *redisStore) Record(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	now := time.Now()
	cutoff := now.Add(-s.retention).UnixMilli()
	pipe := s.rdb.Pipeline()
	for _, sm := range samples {
		t := sm.Time.UnixMilli()
		pipe.Set(ctx, hostKey(sm.ContainerID), sm.HostID, s.retention*2)
		for _, metric := range allMetrics {
			v, _ := metricValue(sm, metric)
			k := key(sm.ContainerID, metric)
			pipe.ZAdd(ctx, k, redis.Z{Score: float64(t), Member: fmt.Sprintf("%d:%g", t, v)})
			pipe.ZRemRangeByScore(ctx, k, "-inf", strconv.FormatInt(cutoff, 10))
			pipe.Expire(ctx, k, s.retention*2)
		}
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (s *redisStore) Query(ctx context.Context, containerID, metric string, since time.Time) ([]Point, error) {
	k := key(containerID, metric)
	members, err := s.rdb.ZRangeByScore(ctx, k, &redis.ZRangeBy{
		Min: strconv.FormatInt(since.UnixMilli(), 10),
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, err
	}
	return parseMembers(members), nil
}

// QueryAll pipelines one ZRangeByScore per container id — still one round
// trip to Redis regardless of how many ids are asked for.
func (s *redisStore) QueryAll(ctx context.Context, metric string, since time.Time, containerIDs []string) (map[string][]Point, error) {
	if len(containerIDs) == 0 {
		return map[string][]Point{}, nil
	}
	zrange := &redis.ZRangeBy{Min: strconv.FormatInt(since.UnixMilli(), 10), Max: "+inf"}
	pipe := s.rdb.Pipeline()
	cmds := make(map[string]*redis.StringSliceCmd, len(containerIDs))
	for _, cid := range containerIDs {
		cmds[cid] = pipe.ZRangeByScore(ctx, key(cid, metric), zrange)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	out := make(map[string][]Point, len(containerIDs))
	for cid, cmd := range cmds {
		members, err := cmd.Result()
		if err != nil || len(members) == 0 {
			continue
		}
		out[cid] = parseMembers(members)
	}
	return out, nil
}

// parseMembers decodes a sorted set's "<ts>:<value>" members into Points,
// silently dropping anything malformed (there is no valid state that
// produces one — Record is the only writer).
func parseMembers(members []string) []Point {
	out := make([]Point, 0, len(members))
	for _, m := range members {
		sep := strings.IndexByte(m, ':')
		if sep < 0 {
			continue
		}
		t, err1 := strconv.ParseInt(m[:sep], 10, 64)
		v, err2 := strconv.ParseFloat(m[sep+1:], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Point{T: t, V: v})
	}
	return out
}

func (s *redisStore) Close() error { return s.rdb.Close() }

func (s *redisStore) HostFor(ctx context.Context, containerID string) (int64, bool, error) {
	v, err := s.rdb.Get(ctx, hostKey(containerID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}
