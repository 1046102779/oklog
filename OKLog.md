## OK Log
[OK Log](https://github.com/oklog/oklog)是一个用于大规模集群的分布式且无协的日志管理系统。我是从一些最基本的原则考虑这个系统的设计的。下面介绍的就是这个原型的思路来源。

### 绪论
过去的一两年时间，我受邀参加很多关于微服务、Go和[Go kit](https://github.com/go-kit/kit)的演讲和研讨会议。选一个微服务架构，意味着要对很多考虑点进行技术选型。如果可能的话，对一些新兴的中等规模系统，我愿意给出一些技术指南。开源社区的项目是非常丰富的。
* 服务编排? 有[Kubernetes](https://github.com/kubernetes/kubernetes)、[nomad](https://github.com/hashicorp/nomad)、DC/OS、ECS等，有很多服务编排工具，都是很好的选择。(ps：目前docker和Kubernetes深度合作了，Mesos可能要被边缘化了)。
* 服务发现? [Consul](https://github.com/hashicorp/consul)、[Etcd](https://github.com/coreos/etcd)、[Zookeeper](http://zookeeper.apache.org/)动态服务发现等工具，也有静态注册和服务发现工具[Linkerd](https://linkerd.io/)；
* 分布式调用链跟踪？[Zipkin](https://github.com/openzipkin/zipkin)、[Jaeger](https://github.com/jaegertracing/jaeger)、Appdash、Lightstep等，它还在爆发式增长.
* 监控工具？[Prometheus](https://github.com/prometheus/prometheus)， 它是目前最强的监控工具、[InfluxDB](https://github.com/influxdata/influxdb)等，它们结合[Grafana](https://grafana.com/)工具使用
* 日志？我陷入了沉思....

很明确的答案似乎是Elastic和ELK技术栈。确实，它很有特点、且入手很容易。但是Elastic被很多人认为，对于中等规模的集群，都很难操作。同时我相信，在全文、基于文档搜索时，Lucene或许不是最好的的数据存储格式。最终，我了解了很多使用Elastic的朋友，由于操作的难度很高，他们中的大多数都不怎么乐意使用它。几乎很少有人使用更高级的特性。

#### 更美好的事物
我认为，对于日志管理系统，应该有一个更好的答案。我问了一些同事，他们正在着手的解决方案。一些同事实际上采用了[Kafka](https://kafka.apache.org/)消息队列解决日志系统管理，特别是对于高QOS和持久化日志要求。但是它的操作也相当难，且最终设计成的日志管理系统，和我感兴趣要解决的问题也不相同。其他人通过数据仓库HBase来解决。但是管理一个Hadoop集群需要更加专业化的只是和非凡的努力。对于这些方案的选择，我认为具体化的或者比较重的系统设计都是一个好的建议。

我还在[Twitter](https://twitter.com/peterbourgon/status/797256574242680832)上提出了这个问题。[Heka](https://github.com/mozilla-services/heka)似乎是最接近我需要的，但是因为作者前期设计错误，导致了16年年底遇到了无法修复的性能问题，已经放弃了Heka的维护，这是一件非常糟糕的事情。[Ekanite](https://github.com/ekanite/ekanite)提供了端到端的解决方案,但是它的系统日志协议与微服务的工作负载有很明显的不匹配。对于日志传送和注解有非常好的工具，例如：[Fluentd](https://github.com/fluent/fluentd)和[Logstash](https://github.com/elastic/logstash)，但是它们只能解决部分问题；它们不能处理存储和日志查询。委托解决方案的工具，有[Splunk](https://www.splunk.com/)和[Loggly](https://www.loggly.com/)，如果你的日志是低容量，且不介意把日志上传到云端，这两个工具都是很好的选择，~~但是它们很快变得昂贵，且无法再本地和开放源代码框中打勾。~~(ps: 这句话不是很明白)。

#### Prometheus日志
我意识到我需要的是Prometheus日志的设计原则。什么意思呢？Prometheus好的地方有什么呢？我的观点：
* 独立运行：它既是开源的、又可以在本地部署
* [云原生](http://dockone.io/article/591)的工作负载：动态的、容器化的和微服务的水平扩展. (ps: 链接中的解释我是非常满意的，是不是就是Serverless)
* 容易操作：本地存储、没有集群、拉模式
* 完善的系统：不需要独立的TSDB(时间序列数据库)、web UI等，容易使用
* 系统扩容：90%的用户承认使用很小的成本，就可以获取比较高的满意度

那Prometheus日志是什么样子的呢？我希望冬天把这个日志管理系统设计完成，我认为这是非常有趣的，同时我也可以学到很多的知识。首先我需要思考得更加深入。

### 设计
#### 高层次目标
首先，像Prometheus一样，系统应该是开源的，且支持本地部署。更重要的是，它应该很容易部署和水平扩展。它应该更加关注容器化的微服务工作负载。同时他应该是一个完善的，端到端的系统，有forwarders、ingesters、storages和query四个特性。

这个日志管理系统关注点：
* 微服务的应用程序日志，包括：debug、info、warn等各种级别日志。这个是典型的高容量、低QOS日志，但是对延时(查询时间)有较高的要求。
* 我们也想服务于事件日志，包括：审计跟踪和点击跟踪等等。这是典型的低容量，搞QOS，但是对延时(查询时间)没有较高的要求。
* 最后，它应该有一个统一的日志消费者，管理来自黑盒的日志输出，例如：mysql服务。也就是说，我们不会控制日志格式。
我相信这样的系统可以服务于所有的需求，同时扩展性也非常好。

心里有了这些目标，我们就需要开始增加一些约束，有了边界才能使问题更加容易处理，关注点更加集中。

#### 问题约束
宝贵的经验告诉我，数据系统应该更多地关注数据传输，同时增加数据的价值。这就是说：
* 它是一个数据运输系统，解决更多的机械问题，黑盒运输
* 它也应该是一个应用系统，提供商业价值，对拓扑和性能要求不需要参与
如果尝试用一个方案解决这两个问题，会造成竞争和一定的妥协。所以我比较感兴趣数据传输系统，旨在解决低吞吐率和延时问题。我们可以使用其他的工具，在系统外部增加数据的商业价值。例如：上下文context可以在ingest之前发生。或者，解析日志再聚合可以在ETLs(数据仓库技术)中完成。然后再使用更加丰富的查询功能的数据系统将其结果视图化。

考虑到这一点，基于时间边界的grep查询接口是完全可接受的。对于新用户，他们经常想要一个熟悉的接口来帮助他们调试-“我想要grep我的日志”，这是非常有用的。构建ETLs(数据仓库技术)到更复杂的系统中是完全足够的。总之，这个日志管理系统是一个基本的、底层系统，它可以和其他工具搭配使用，至于搭配什么样的工具，主要看你自己的需求。(ps: 类似于系统插件化)


