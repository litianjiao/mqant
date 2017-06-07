// Copyright 2014 mqant Author. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package defaultrpc

import (
	"fmt"
	"github.com/liangdas/mqant/conf"
	"github.com/liangdas/mqant/log"
	"github.com/streadway/amqp"
	"github.com/liangdas/mqant/rpc/pb"
	"github.com/golang/protobuf/proto"
	"github.com/liangdas/mqant/rpc"
	"runtime"
)

type AMQPServer struct {
	call_chan chan mqrpc.CallInfo
	Consumer  *Consumer
	done      chan error
}

func NewAMQPServer(info *conf.Rabbitmq, call_chan chan mqrpc.CallInfo) (*AMQPServer, error) {
	var queueName = info.Queue
	var key = info.BindingKey
	var exchange = info.Exchange
	c, err := NewConsumer(info, info.Uri, info.Exchange, info.ExchangeType, info.ConsumerTag)
	if err != nil {
		log.Error("AMQPServer connect fail %s", err)
		return nil, err
	}

	//log.Printf("declared Exchange, declaring Queue %q", queueName)
	queue, err := c.channel.QueueDeclare(
		queueName, // name of the queue
		true,      // durable	持久化
		false,     // delete when unused
		false,     // exclusive
		false,     // noWait
		nil,       // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("Queue Declare: %s", err)
	}

	//log.Printf("declared Queue (%q %d messages, %d consumers), binding to Exchange (key %q)",
	//	queue.Name, queue.Messages, queue.Consumers, key)

	if err = c.channel.QueueBind(
		queue.Name, // name of the queue
		key,        // bindingKey
		exchange,   // sourceExchange
		false,      // noWait
		nil,        // arguments
	); err != nil {
		return nil, fmt.Errorf("Queue Bind: %s", err)
	}
	//log.Printf("Queue bound to Exchange, starting Consume (consumer tag %q)", c.tag)
	deliveries, err := c.channel.Consume(
		queue.Name, // name
		c.tag,      // consumerTag,
		false,      // noAck 自动应答
		false,      // exclusive
		false,      // noLocal
		false,      // noWait
		nil,        // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("Queue Consume: %s", err)
	}
	server := new(AMQPServer)
	server.call_chan = call_chan
	server.Consumer = c
	server.done = make(chan error)
	go server.on_request_handle(deliveries, server.done)

	return server, nil
	//log.Printf("shutting down")
	//
	//if err := c.Shutdown(); err != nil {
	//	log.Fatalf("error during shutdown: %s", err)
	//}
}

/**
停止接收请求
*/
func (s *AMQPServer) StopConsume() error {
	return s.Consumer.Cancel()
}

/**
注销消息队列
*/
func (s *AMQPServer) Shutdown() error {
	return s.Consumer.Shutdown()
}

func (s *AMQPServer) Callback(callinfo mqrpc.CallInfo) error {
	body, _ := s.MarshalResult(callinfo.Result)
	return s.response(callinfo.Props, body)
}

/**
消息应答
*/
func (s *AMQPServer) response(props map[string]interface{}, body []byte) error {
	var err error
	if err = s.Consumer.channel.Publish(
		"", // publish to an exchange
		props["reply_to"].(string), // routing to 0 or more queues
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			Headers:         amqp.Table{},
			ContentType:     "text/plain",
			ContentEncoding: "",
			Body:            body,
			DeliveryMode:    amqp.Transient, // 1=non-persistent, 2=persistent
			Priority:        0,              // 0-9
			// a bunch of application/implementation-specific fields
		},
	); err != nil {
		log.Warning("Exchange Publish: %s", err)
		return err
	}
	return nil
}

/**
接收请求信息
*/
func (s *AMQPServer) on_request_handle(deliveries <-chan amqp.Delivery, done chan error) {
	defer func() {
		if r := recover(); r != nil {
			var rn = ""
			switch r.(type) {

			case string:
				rn = r.(string)
			case error:
				rn = r.(error).Error()
			}
			buf := make([]byte, 1024)
			l := runtime.Stack(buf, false)
			errstr := string(buf[:l])
			log.Error("%s\n ----Stack----\n%s",rn,errstr)
		}
	}()
	for {
		select {
		case d, ok := <-deliveries:
			if !ok {
				deliveries = nil
			} else {
				//log.Printf(
				//		"got %dB on_request_handle delivery: [%v] %q",
				//		len(d.Body),
				//		d.DeliveryTag,
				//		d.Body,
				//)
				d.Ack(false)
				rpcInfo, err := s.Unmarshal(d.Body)
				if err == nil {
					callInfo:=&mqrpc.CallInfo{
						RpcInfo:*rpcInfo,
					}
					callInfo.Props = map[string]interface{}{
						"reply_to": callInfo.RpcInfo.ReplyTo,
					}

					callInfo.Agent = s //设置代理为AMQPServer

					s.call_chan <- *callInfo
				} else {
					fmt.Println("error ", err)
				}

			}
		case <-done:
			s.Consumer.Shutdown()
			break
		}
		if deliveries == nil {
			break
		}
	}
}

func (s *AMQPServer) Unmarshal(data []byte) (*rpcpb.RPCInfo, error) {
	//fmt.Println(msg)
	//保存解码后的数据，Value可以为任意数据类型
	var rpcInfo rpcpb.RPCInfo
	err := proto.Unmarshal(data, &rpcInfo)
	if err != nil {
		return nil, err
	} else {
		return &rpcInfo, err
	}

	panic("bug")
}
// goroutine safe
func (s *AMQPServer) MarshalResult(resultInfo rpcpb.ResultInfo) ([]byte, error) {
	//log.Error("",map2)
	b, err := proto.Marshal(&resultInfo)
	return b, err
}

