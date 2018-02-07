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

### 4. pkg/store
文件名 | 参数(interface-struct-method) | 相关说明 
---|---|---
file_log.go | fileLog struct | 每一个fileLog实体代表一个segment文件, 实现了Create, Query, Close, Stats, Overlapping, Purgeable(删除比传入时间小的Trashed端文件列表), Squential(返回有序的segment段至少两个), Trashable(垃圾回收比传入时间小的所有flushed段文件，并把这些文件改为以后缀.trashed结尾的文件, 也就是垃圾回收站), queryMatchingSegments方法, 其中Query方法根据参数返回segment列表的io.ReaderCloser, 作用是每行都要和时间比较，同时内容Contains为真,才返回记录结果列表
||chooseFirstSequential方法 | 有序的segment列表，并选取连续小segment列表文件大小和，且大于指定的最小文件数量。如果当文件数量小于指定的最小文件数量时，则清空，继续往下计算, 作者设置默认最小文件数2个
||recoverSegments方法 | 恢复active和reading段，并改为flushed段
||recordFilterPlain方法 | 给定的记录或者比特流是否包含query查询的参数值 
||recordFilterRegex方法 | 给定的记录或者比特流记录是否符合query查询的正则参数值
||recordFilterBoundedPlain方法 | 带时间边界记录列表的query查询的参数值
||recordFilterBoundedRegex方法 | 带时间边界记录列表的query查询的正则参数值
||fileWriteSegment struct|  包括Write，Close(关闭并把文件名改为以后缀.flushed结尾), Delete(关闭并删除文件)
||fileReadSegment struct| 包括Read，Reset(关闭并把文件名改为以后缀.flushed结尾), Trashed(关闭并把文件名改为以后缀.trashed结尾), Purge(关闭并删除文件)
||fileTrashSegment struct | 包括Purge(关闭并删除文件)
||segmentInfo struct | 段文件
||basename方法| 获取绝对或者相对路径的文件名前缀
||modifyExtension方法| 改变文件名的后缀名
||parseFilename方法 | 解析文件名，返回low和high. store节点的文件名命名规则：lowULID-highULID.{active, reading, flushed, trashed}, 其中lowULID第一条记录，highULID最后一条记录
||segmentParseError struct | 实现了Error方法
api.go | ClusterPeer interface | 两个方法：1.获取指定类型的节点列表；2.获取各个节点的当前状态
||Doer interface| 一个方法：客户端发起http请求Do方法
||API struct | 结构体包含了ClusterPeer, QueryClient(客户端查询立即返回，且有超时)和StreamClient(客户端查询流式返回，不会超时), 以及Log段文件存储文件类型
||API.Close方法| 关闭流式查询
||API.ServeHTTP方法| 查询处理网关, dispatch处理`/query`(handleUserQuery), `/_query`, /stream, `/_stream`, `/replicate`, `/_clusterstate`请求
||API.handleUserQuery方法 | 处理外部用户查询请求, 通过store节点类型获取gossip集群中的节点列表，并构建新的请求`_query`请求分发到所有获取的store节点处理, 然后获取响应数据并返回reader给ResponseWriter
||API.handleInternalQuery方法 | 处理内部查询请求， 调度file_log.go中的Query方法, 并封装返回结果
||API.handleUserStream方法 | 处理外部用户流式查询请求，获取store节点类型的节点列表，并构建新的请求`/_stream`请求分发到所有的store节点列表处理，然后流式返回匹配的数据到响应client中
||API.handleInternalStream方法 |  处理内部流式查询请求，然后流式返回给client
||API.handleReplicate方法 |  复制segment(request.Body)，并修改文件名为uuid.active
||API.handleClusterState方法 |  返回当前gossip集群的当前状态
||interceptingWriter struct | 截获并封装ResponseWriter, 有WriteHeader, Flush两个方法
||teeRecords方法 | 复制segment文件的Reader数据到Writer中，形成一个新的段文件
