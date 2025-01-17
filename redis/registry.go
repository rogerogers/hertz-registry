// Copyright 2022 CloudWeGo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package redis

import (
	"context"
	"sync"

	"github.com/cloudwego/hertz/pkg/app/server/registry"
	"github.com/go-redis/redis/v8"
)

var _ registry.Registry = (*redisRegistry)(nil)

type redisRegistry struct {
	client *redis.Client
	rctx   *registryContext
	mu     sync.Mutex
	wg     sync.WaitGroup
}

type registryContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRedisRegistry creates a redis registry
func NewRedisRegistry(addr string, opts ...Option) registry.Registry {
	redisOpts := &redis.Options{
		Addr:     addr,
		Password: "",
		DB:       0,
	}
	for _, opt := range opts {
		opt(redisOpts)
	}
	rdb := redis.NewClient(redisOpts)
	return &redisRegistry{
		client: rdb,
	}
}

func (r *redisRegistry) Register(info *registry.Info) error {
	if err := validateRegistryInfo(info); err != nil {
		return err
	}
	rctx := registryContext{}
	rctx.ctx, rctx.cancel = context.WithCancel(context.Background())
	m := newMentor()
	r.wg.Add(1)
	go m.subscribe(rctx.ctx, info, r)
	r.wg.Wait()
	rdb := r.client
	hash, err := prepareRegistryHash(info)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.rctx = &rctx
	r.mu.Unlock()
	keys := []string{
		hash.key,
	}
	args := []interface{}{
		hash.field,
		hash.value,
		defaultExpireTime,
		generateMsg(register, info.ServiceName, info.Addr.String()),
	}
	err = registerScript.Run(rctx.ctx, rdb, keys, args).Err()
	if err != nil && err != redis.Nil {
		return err
	}
	go m.monitorTTL(rctx.ctx, hash, info, r)
	go keepAlive(rctx.ctx, hash, r)
	return nil
}

func (r *redisRegistry) Deregister(info *registry.Info) error {
	if err := validateRegistryInfo(info); err != nil {
		return err
	}
	rctx := r.rctx
	rdb := r.client
	hash, err := prepareRegistryHash(info)
	if err != nil {
		return err
	}
	keys := []string{
		hash.key,
	}
	args := []interface{}{
		hash.field,
		generateMsg(deregister, info.ServiceName, info.Addr.String()),
	}
	err = deregisterScript.Run(rctx.ctx, rdb, keys, args).Err()
	if err != nil && err != redis.Nil {
		return err
	}
	rctx.cancel()
	return nil
}

var registerScript = redis.NewScript(`
local key = KEYS[1]
local field = ARGV[1]
local value = ARGV[2]
local expireTime = tonumber(ARGV[3])
local message = ARGV[4]

redis.call('HSET', key, field, value)
redis.call('EXPIRE', key, expireTime)
redis.call('PUBLISH', key, message)
`)

var deregisterScript = redis.NewScript(`
local key = KEYS[1]
local field = ARGV[1]
local message = ARGV[2]

redis.call('HDEL', key, field)
redis.call('PUBLISH', key, message)
`)
