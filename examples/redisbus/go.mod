module github.com/ant-caor/nimbus/examples/redisbus

go 1.25.0

require (
	github.com/ant-caor/nimbus v0.0.0
	github.com/redis/rueidis v1.0.75
)

require (
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

replace github.com/ant-caor/nimbus => ../..
