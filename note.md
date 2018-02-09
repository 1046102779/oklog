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
|read.go| mergeRecordsToLog方法 | 这个方法蛮有意思的。 重点介绍
|compact.go| Compact struct| 结构体元素：Log,合并后的段文件segmentTargetSize , 保留时间，清除时间
||Compact.Run方法|和作者写的Group思路一致，goroutine生命周期维护. 该方法主要是每秒做四个操作，获取时间重叠最多的连续segment列表, 序列化， 移动到垃圾回收站，删除段文件列表
||Compact.compact方法|作用：根据传入参数kind，获取指定的段文件列表，然后调用mergeRecordsToLog方法，合并指定kind获取的segment列表，形成多个段文件, 最后做两个收尾操作。注意两点：1. 如果合并段文件出错，则回滚传入的segment列表；2.合并成功，则删除传入的segment列表
||Compact.moveToTrash方法| 获取当前时间减去retain保留时间，然后根据文件命名规则的high位值比较大小，获取小于该值的所有段列表, 并遍历移动到垃圾回收站.trashed
||Compact.emptyTrash方法 | 获取当前时间减去purge清除时间，然后根据文件命名规则的high位值比较大小，获取小于该值的所有段列表，并遍历删除.trashed段文件
|consume.go| - | 作用： 主动消费ingesters节点产生的日志数据，并通过状态机state machine{gather segments, replicate, commit , repeat}四种状态来实现
||Consume struct|结构体元素：Peer集群, pull拉取ingest节点数据的client, 段文件size，段文件切割时间值，复制因子, map pending, active buffer
||Consume.Run方法|和作者写的Group思路一致，同时通过返回state函数值，完成了状态机的状态转换，初始状态设置为gather
||Consume.gather方法| 蛮有意思的，重点介绍
||mergeRecords方法| 和mergeRecordsToLog相似，重点介绍下
||Consume.replicate方法| 通过PeerTypeStore类型，从peer集群中获取store节点列表，然后store节点随机化并与上复制因子，for循环把Consume中的active buffer的数据通过http请求写入到store节点中
||Consume.commit方法| 调用Consume.resetVia方法，类型：commit
||Consume.fail方法| 调用Consume.resetVia方法，类型：failed
||Consume.resetVia方法| 通过指定具体类型：commit或者failed, 通过在gather阶段的pending存储，来遍历ingest和日志记录ID, 并通过http请求ingest节点进行提交或者回滚。多少条记录，就会有对应数量的goroutine并发, 然后清空Consume的各个相应元素值，比如：pending, active buffer等
|event.go| EventReporter interface | 只有一个ReportEvent(event)方法, 目前用于store日志输出, 注意：Event struct构成：Debug, Op, File, Error, Warning, Msg, 这种结构可以作为以后的日志格式参考方式, 挺好的
||LogReporter struct | ReportEvent方法实现了EventReporter接口, 用于终端日志输出，现在继承了go-kit/kit/log
|log.go | Log interface| 该接口为store节点提供了日志段文件服务有：Create(创建store段文件), Query(搜索查询), Overlapping(获取.flushed连续数量最多的重叠段), Sequential(获取有序且连续段文件数量, 用于合并段文件), Trashable(把小于指定时间的所有段文件放入到回收站), Purgeable(把小于指定时间的所有.trashed段文件列表，全部删除), Stats(统计当前的active、pending、flushed和trashed段各个size)和Close(释放段)方法
||WriteSegment interface| 继承了io.Writer interface, 并提供了Close, Delete方法
||ReadSegment interface|继承了io.Reader interface, 并提供了Reset, Trash, Purge方法
||TrashSegment interface| 提供了Purge方法
||LogStats struct | 元素包括：ActiveSegments段文件数量, ActiveBytes段文件大小, FlushedSegments段文件数量, FlushedBytes段文件大小, ReadingSegments段文件数量和ReadingBytes段文件大小，TrashedSegments段文件数量和TrashedBytes段文件大小
|overlap.go|overlap方法| 参数两组：一组边界a，一组与边界计算是否重合的组b, 返回a与b是否有重叠
|query.go| QueryParams struct| 结构体元素：(From, To)起始时间，Q 查询，Regex 是否为正则匹配
||QueryParams.DecodeFrom方法 | 解析url请求参数，包括From、To、Q和Regex
||ulidOrTime struct|结构体：ulid.ULID和time.Time, 它主要用来解析传入的From或者To参数，为ULID和time
||ulidOrTime.Parse方法|解析时间字符串From和To参数, 其中支持的ULID的时间格式，和time.RFC3339Nano时间两种格式
||QueryResult struct| 元素：QueryParams查询参数，NodesQueried查询的节点数, SegmentsQueried查询段文件数量, MaxDataSetSize总共查询的总大小size, ErrorCount错误段文件数量, Duration和Records返回的段io.Reader
||QueryResult.EncodeTo方法|服务端封装QueryResult各个元素值，并输出ResponseWriter
||QueryResult.DecodeFrom方法|客户端解析ResponseWriter，并存储到QueryResult中。`注意：这些QueryResult各个元素参数在http通信过程中都是存储在http.Header中，例如：X-Oklog-From, X-Oklog-Max-Data-Set-Size等`
||QueryResult.Merge方法|合并两个QueryResult,  最后把各自的io.ReadCloser合并成有序的段文件buffer, 然后赋给QueryResult.Records. `这里有个蛮有意思的点：buffer并非io.ReadCloser，所以buffer不能直接赋值给QueryResult.Records, 需要提供一个io.Closer方法，所以io.NopCloser就实现了这一点`
|query_registry.go|-|主要用来进行流式查询，也即stream查询方式，不超时
|| queryRegistry struct| 元素包括：mutex, chanmap(多查询channel返回符合搜索结果的流式输出)
|| queryRegistry.Register方法| 注册查询
||queryRegistry.Close方法|关闭所有查询的chanmap通道
||queryRegistry.Match方法|

### 重点方法方法：mergeRecordsToLog
形参：Log, segmentTargetSize, []io.Reader

主要作用，针对传入的readers参数，遍历该readers列表获取各自的scan，取出每一行record且获取它的id(ulid.New构成), 比较获取最小的记录写入Log段文件中，当段文件大小大于等于segmentTargetSize后，关闭文件且重新创建新的段文件。 直到完成readers段文件内容全部读完

注意一点，因为每个段文件需要命名为low-high.flushed, 所有low为第一条记录的ID，high为最后一条记录的ID, 同时写入一条最小记录后，这个段文件需要通过(advance方法)偏移一行. 

这个算法是个优化点

### 重点方法方法：Consume.gather
作用：主动拉取ingest节点日志记录，并写入active buffer缓冲区，如果超出时间和size大小，则状态置为：replicate
算法步骤：
1. 通过PeerTypeIngest获取peer集群中的ingest节点列表；
2. 从ingest节点列表中, 随机选取一个ingest节点，并首先通过http请求ingest节点，获取下一个读取的记录ID;
3. 通过选中的ingest和返回的记录ID，通过http请求ingest节点的记录ID，获取该ID的日志记录数据
4. 最后通过mergeRecords方法，从返回的resp.Body中读取记录并有序写入到active buffer中
5. 如果active buffer大小和存在时间，有超过设置的段文件大小和age的，转入replicate状态

### 重点方法介绍: mergeRecords
形参：io.Writer, []io.Reader
这个方法和mergeRecordsToLog很相似，只是mergeRecords方法把[]io.Reader各个数据流按照时间有序化地写入io.Writer中，并返回io.Writer流中的low和high的ID
