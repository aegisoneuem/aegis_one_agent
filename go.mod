module github.com/aegisoneuem/aegis_one_agent

go 1.27.1

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/aegisoneuem/aegis_one_proto v0.0.0-00010101000000-000000000000
	github.com/go-ole/go-ole v1.3.0
	golang.org/x/sys v0.47.0
	google.golang.org/grpc v1.84.0
)

require (
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/aegisoneuem/aegis_one_proto => ../aegis_one_proto
