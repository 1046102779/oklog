## OK Log
OK Log是一个分布式、且无协同的日志管理系统，它是由peterbourgon大牛设计的，我最近想找一个日志搜集系统，用于监控和报警。用了两年的heka，去年年底因为性能问题导致作者rafrombrc放弃了Go版项目

我这两周准备整理和翻译OK Log相关文档，然后再动手部署并搭建个DEMO，如果好用，我再部署的测试环境中，后续持续更新....

## 集群使用手册
oklog通过数据传输形式，把各个节点串联起来，形成一个完备的分布式日志管理系统。它包括forward数据采集节点, ingest、store聚集和合并节点，query查询节点。且每一个节点都可以多实例，水平扩展, 集群节点之间的通信采用gossip协议。现实中的节点图:

```shell
-------------------------------------------------------------------------------------------------------------------
|  -----------                        ----------                      ----------                     ----------   |
|  | forward |  ------          |---- | ingest | -----          ---- | store   |-----          ---- | query   |-  |
|  -----------       |          |     ----------      |         |     ----------    |          |     ----------   |
|                    |          |                     |         |                   |          |                  |
|  -----------       |          |    ------------     |         |    ------------   |          |    ------------  |
|  | forward |  -----|->----->- -----|  ingest  |-->------->--- |----|  store  |----|-<----<---|----|  query  |-  |
|  -----------       |          |    ------------     |         |    ------------   |          |    ------------  |
|    .....           |          |       .....         |         |       .....       |          |       .....      |
|                    |          |                     |         |                   |          |                  |
|  -----------       |          |     -----------     |         |    |-----------   |          |    |-----------  |
|  | forward | ------|          |----|  ingest  |-----|         |----|  store  |------         |----| query  |-   |
|  -----------                       |-----------                    |-----------                   |-----------  |
|                                                                                                                 |
|                                                                                                                 |
-------------------------------------------------------------------------------------------------------------------
```


forward/ingest/store/query各个节点都是可以水平扩展，每个节点都可以对外提供服务, 由IP:PORT构成

每一个同类节点组成一个小集群、四个小集群组成一个大集群

## DEMO搭建步骤

### 实验环境
1. golang1.9.2 , ubuntu14.04 , 4CPU+8G内存
2. IP地址：10.6.1.101

### 实验准备工作：
1. 在同一台机器上搭建三个ingest/store节点, 一个forward节点和一个query节点
```golang
1. 一个forward节点, 作为日志采集服务
2. ingeststore小集群的服务地址分别是10.6.1.101:7159, 10.6.1.101:7259, 10.6.1.101:7359
3. ingeststore节点的api、fast、durable和bulk分别是7X50, 7X51, 7X52, 7X53
```
```golang
git clone git@github.com:1046102779/oklog.git

cd ~/godev/src/github.com/oklog/oklog/cmd/oklog/

go build && go install // go build会报错，很多包还需要进行go get

## ps：在oklog目录下 pkg/cluster/peer.go 第85行, 感觉是一个显示缺陷的bug, 因为不去掉最先起来的小集群节点无法显示其他节点内容

暂时去掉了条件语句：if len(existing) > 0 {}

oklog ingeststore -store.segment-replication-factor 1 -cluster=tcp://10.6.1.101:7159   -api=tcp://10.6.1.101:7150 -ingest.fast=tcp://10.6.1.101:7151 -ingest.durable=tcp://10.6.1.101:7152 -ingest.bulk=tcp://10.6.1.101:7153
## store.segment-replication-factor 表示日志备份数量
## 还有其他参数，比如日志文件的切割维度：时间和大小, 有默认值1天和128M

oklog ingeststore -store.segment-replication-factor 1 -cluster=tcp://10.6.1.101:7259  -peer=10.6.1.101:7159  -api=tcp://10.6.1.101:7250 -ingest.fast=tcp://10.6.1.101:7251 -ingest.durable=tcp://10.6.1.101:7252 -ingest.bulk=tcp://10.6.1.101:7253

oklog ingeststore -store.segment-replication-factor 1 -cluster=tcp://10.6.1.101:7359  -peer=10.6.1.101:7159 -peer=10.6.1.101:7259   -api=tcp://10.6.1.101:7350 -ingest.fast=tcp://10.6.1.101:7351 -ingest.durable=tcp://10.6.1.101:7352 -ingest.bulk=tcp://10.6.1.101:7353

日志数据来源main.go的demo, forward采集数据:
./temp  | oklog forward -prefix="FREEGO_WORK"  tcp://10.6.1.101:7151 tcp://10.6.1.101:7251 tcp://10.6.1.101:7351
## prefix表示前缀，这个真的很好，不同项目可以把日志放在一起存储了
## tcp://10.6.1.101:7X51 表示ingest集群服务节点, 也可以只指定一个
## 当ingeststore集群起来时，会生成小集群各自的data/ingest, data/store日志文件目录

日志查询：
oklog query -store tcp://10.6.1.101:7150 -from 5m -q "lily order" 
## -store参数：查询某个指定节点的日志数据, 当ingeststore小集群数量等于日志的备份数量时，任何一个节点都可以查到想要的数据
## 这里我还有个疑问需要去研究下：当集群节点的数量大于日志的备份数量时，如果查询节点不能指定多个store节点的话，有可能日志生成，但是查不到
## 日志查询UI：http://10.6.1.101:7X50/ui
```

```golang
package main

import (
    "fmt"
    "time"
)

func main() {
    fmt.Println("hello,world")
    fmt.Println("vim-go")
    for i := 0; i < 1000; i++ {
        fmt.Printf("generate order: %d\n", i+1)
        if i == 999 {
            i = 0
        }
        time.Sleep(time.Second)
    }
}
```

## 接下来的事情
这个只是把oklog跑起来了，也不算吧，后续的工作

* OKLog作者是跑在docker里的，如果不懂docker的，可以用我的oklog这个demo, 所以不支持go get github.com/oklog/oklog
* OKLog架构上，我理解可能有些错误，还需要进一步学习和了解
* 深入了解下各类节点的参数
* 现在oklog的forward节点只是用了终端产生的日志，大多数时候我们是产生的日志文件，如果不想改造现有的日志系统，那需要做文件流的日志采集
* 更深入的了解OKLog，这个OKLog有很多可以学习的知识


ps: 共同学习、共同进步
