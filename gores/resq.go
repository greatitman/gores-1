package gores

import (
  "encoding/json"
  "fmt"
  "os"
  "errors"
  "strconv"
  "time"
  "github.com/garyburd/redigo/redis"
  "github.com/deckarep/golang-set"
  "gopkg.in/oleiade/reflections.v1"
)
// redis-cli -h host -p port -a password

const QUEUE_PREFIX = "resq:queue:%s"
const WATCHED_QUEUES = "resq:queues"
const WATCHED_WORKERS = "resq:workers"
const WATCHED_STAT = "resq:stat:%s"

type ResQ struct {
  pool *redis.Pool
  _watched_queues mapset.Set
  Host string
}

func InitPool() *redis.Pool{
    pool := &redis.Pool{
        MaxIdle: 5,
        IdleTimeout: 240 * time.Second,
        Dial: func () (redis.Conn, error) {
                c, err := redis.Dial("tcp", os.Getenv("REDISURL"))
                if err != nil {
                    return c, nil
                }
                c.Do("AUTH", os.Getenv("REDIS_PW"))
                c.Do("SELECT", "gores")
                return c, nil
              },
        TestOnBorrow: func(c redis.Conn, t time.Time) error {
            _, err := c.Do("PING")
            return err
          },
    }
    return pool
}

func NewResQ() *ResQ {
    pool := InitPool()
    if pool == nil {
        fmt.Printf("InitPool() Error\n")
        return nil
    }
    return &ResQ{
              pool: pool,
              _watched_queues: mapset.NewSet(),
              Host: os.Getenv("REDISURL"),
            }
}

func (resq *ResQ) Push(queue string, item interface{}) error{
    conn := resq.pool.Get()

    _, err := conn.Do("RPUSH", fmt.Sprintf(QUEUE_PREFIX, queue), resq.Encode(item))
    if err != nil{
        err = errors.New("Invalid Redis RPUSH Response")
    }
    if err != nil{
        return err
    }
    err = resq.watch_queue(queue)
    if err != nil{
        return err
    }
    return nil
}

func (resq *ResQ) Pop(queue string) map[string]interface{}{
    var decoded map[string]interface{}

    conn := resq.pool.Get()
    reply, err := conn.Do("LPOP", fmt.Sprintf(QUEUE_PREFIX, queue))
    if err != nil || reply == nil {
        return decoded
    }

    data, err := redis.Bytes(reply, err)
    if err != nil{
        return decoded
    }
    decoded = resq.Decode(data)
    if decoded != nil{
        decoded["Struct"] = queue
    }
    return decoded
}

func (resq *ResQ) Decode(data []byte) map[string]interface{}{
    var decoded map[string]interface{}
    if err := json.Unmarshal(data, &decoded); err != nil{
        return decoded
    }
    return decoded
}

func (resq *ResQ) Encode(item interface{}) string{
    b, err := json.Marshal(item)
    if err != nil{
        return ""
    }
    return string(b)
}

func (resq *ResQ) Size(queue string) int64 {
    conn := resq.pool.Get()
    size, err:= conn.Do("LLEN", fmt.Sprintf(QUEUE_PREFIX, queue))
    if size == nil || err != nil {
        return 0
    }
    return size.(int64)
}

func (resq *ResQ) watch_queue(queue string) error{
    if resq._watched_queues.Contains(queue){
        return nil
    } else {
        conn := resq.pool.Get()
        _, err := conn.Do("SADD", WATCHED_QUEUES, queue)
        if err != nil{
            err = errors.New("watch_queue() SADD Error")
        }
        return err
    }
}

func (resq *ResQ) Enqueue(item interface{}) error{
    /*
    Enqueue a job into a specific queue. Make sure the struct you are
    passing has **queue**, **Args** attribute and a **perform** method on it.
    */
    hasQueue, _ := reflections.HasField(item, "Queue")
    hasArgs, _ := reflections.HasField(item, "Args")
    if !hasQueue || !hasArgs {
        return errors.New("unable to enqueue job with struct")
    } else {
        queue, _ := reflections.GetField(item, "Queue")
        err := resq.Push(queue.(string), item)
        return err
    }
}

func (resq *ResQ) Queues() []string{
    conn := resq.pool.Get()
    data, _ := conn.Do("SMEMBERS", WATCHED_QUEUES)
    queues := make([]string, 0)
    for _, q := range data.([]interface{}){
        queues = append(queues, string(q.([]byte)))
    }
    return queues
}

func (resq *ResQ) Workers() []string {
    conn := resq.pool.Get()
    data, _ := conn.Do("SMEMBERS", WATCHED_WORKERS)
    workers := make([]string, 0)
    for _, w := range data.([]interface{}) {
        workers = append(workers, string(w.([]byte)))
    }
    return workers
}


func (resq *ResQ) Info() map[string]interface{} {
    var pending int64 = 0
    for _, q := range resq.Queues() {
        pending += resq.Size(q)
    }

    info := make(map[string]interface{})
    info["pending"] = pending
    info["processed"] = NewStat("processed", resq).Get()
    info["queues"] = len(resq.Queues())
    info["workers"] = len(resq.Workers())
    info["failed"] = NewStat("falied", resq).Get()
    info["host"] = resq.Host
    return info
}



type Stat struct{
    Name string
    Key string
    Resq *ResQ
}

func NewStat(name string, resq *ResQ) *Stat {
    return &Stat{
              Name: name,
              Key: fmt.Sprintf(WATCHED_STAT, name),
              Resq: resq,
          }
}

func (stat *Stat) Get() int64 {
    conn := stat.Resq.pool.Get()
    data, err := conn.Do("GET", stat.Key)
    if err != nil || data == nil{
      return 0
    }
    res, _ := strconv.Atoi(string(data.([]byte)))
    return int64(res)
}

func (stat *Stat) Incr() int{
    _, err:= stat.Resq.pool.Get().Do("INCR", stat.Key)
    if err != nil{
        return 0
    }
    return 1
}

func (stat *Stat) Decr() int {
    _, err:= stat.Resq.pool.Get().Do("DECR", stat.Key)
    if err != nil{
        return 0
    }
    return 1
}

func (stat *Stat) Clear() int{
    _, err:= stat.Resq.pool.Get().Do("DEL", stat.Key)
    if err != nil{
      return 0
    }
    return 1
}