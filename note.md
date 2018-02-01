## 目录学习笔记
###  概论
oklog的集群是由ingest类型节点和store类型节点组成的，数据一致性是通过gossip协议保证的. 其中store节点定时拉取满足size或者timer的段文件，并同步到所有其他store节点

###  pkg/cluster
1.  [memberlist](https://github.com/hashicorp/memberlist)
这个是gossip协议的go版本实现，建议先看一下[如何使用memberlist?](https://github.com/asim/memberlist), 非常有帮助. 注意demo里面delegate的使用, 懂了，就可以继续看oklog如何在各个节点进行数据共享了

```shell
1. ingest多节点和store多节点组成的集群，是通过gossip协议达到数据一致性的。
2. ingest和store属于两类节点，通过类型区分PeerTypeIngest|PeerTypeStore|PeerTypeIngestStore, 通过peer.Current[PeerType]获取某一种类型的集群子集列表
```
2. 
func CalculateAdvertiseIP(bindHost, advertiseHost string, resolver Resolver, logger log.Logger) (net.IP, error) 

用途： 用于gossip协议通信病毒传播的内部通信IP，这个方法是挑选最优的IP，进行节点数据扩散, 一般选择自己作为服务数据传播者

@params:
    bindHost string 可空
    advertiseHost string 可空, 其中advertiseHost参数优先级比bindHost高

2.
