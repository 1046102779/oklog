## 目录学习笔记
###  概论
oklog的集群是由ingest类型节点和store类型节点组成的，数据一致性是通过gossip协议保证的. 其中store节点定时拉取满足size或者timer的段文件，并同步到所有其他store节点

###  1. pkg/cluster
1.  [memberlist](https://github.com/hashicorp/memberlist)
这个是gossip协议的go版本实现，建议先看一下[如何使用memberlist?](https://github.com/asim/memberlist), 非常有帮助. 注意demo里面delegate的使用, 懂了，就可以继续看oklog如何在各个节点进行数据共享了

 - ingest多节点和store多节点组成的集群，是通过gossip协议达到数据一致性的。
 - ingest和store属于两类节点，通过类型区分PeerTypeIngest|PeerTypeStore|PeerTypeIngestStore, 通过peer.Current[PeerType]获取某一种类型的集群子集列表

2. func CalculateAdvertiseIP(bindHost, advertiseHost string, resolver Resolver, logger log.Logger) (net.IP, error) 

用途： 用于gossip协议通信病毒传播的内部通信IP，这个方法是挑选最优的IP，进行节点数据扩散, 一般选择自己作为服务数据传播者

@params:

    bindHost string 可空

    advertiseHost string 可空, 其中advertiseHost参数优先级比bindHost高

3. peers.go文件主要用来进行gossip内部通信传递节点对外服务的IP:PORT的. 也就是说，gossip中的每个节点都是可以获取到store类型所有节点或者ingest类型所有节点对外服务的地址和端口的

### 2. pkg/fs
oklog文件系统支持nop(空文件系统)、virtual(内存文件系统)、real(本地文件系统)。统一实现FileSystem, File, Releaser三个接口。

如果采用了nop文件系统，则所有oklog是一个空跑日志系统；采用virtual文件系统，则所有日志存放在内存中，进行内存日志查询；采用real文件系统，则日志全部落到磁盘文件中

通常采用real文件系统，作为后端存储

### 3. pkg/ingest
文件名 | 参数(interface-struct-method) | 相关说明 
---|---|---
log.go | Log interface | Log接口主要包括写segment，读segment，所有的段统计信息和关闭segment
| | WriteSegment interface | io.Writer, Sync(同步到fs), 关闭和删除file
| | ReadSegment interface | io.Reader, Commit(提交并删除pendingSegment), 处理失败并删除pendingSegment, 获取段size
| | LogStats struct |  活跃段数量及字节数, 写入到磁段数量及字节数, 正在写的段数量及字节数
api.go | 该文件主要用于处理来自http请求各个阶段对pending map中的segment的处理请求 | 包括commit, failed. next, read, 正在写段的所有统计，集群中所有ingest节点的统计等
| | - | 这里要强调一点，ingest处理的所有请求采用了group通用处理模式，对goroutine的生命周期进行管理。
| | next | next作用：从Log interface的Oldest()方法获取一个ReadSegment，并存入pending map中
| | read | read作用：从pending map中取出key=id的ReadSegment，并把Read状态置为true，正在读取中
| | commit | commit作用：从pending map中取出key=id的ReadSegment，Commit, 并删除且返回该段的size
| | fail | fail作用：从pending map中取出key=id的ReadSegment，Failed, 并删除
conn.go | connectionManager struct | 作用：管理所有来自forward请求连接active map, 并从连接上以`\n`为分割符获取日志流数据，并保存到writer中的log日志流中
writer.go | Writer struct | 底层是fs(vitual, nop, real)，上层封装主要作用：segment依赖于时间和size两个维度，进行segment的构建(分割日志文件)
file_log.go | fileLog struct | 实现了Log interface接口
| | fileReadSegment struct | 实现了ReadSegment interface接口{ Commit, Failed, Read和Size方法}
| | fileWriteSegment struct | 实现了WriteSegment interface接口 {Close, Delete, Sync, Write}
