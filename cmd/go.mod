module github.com/goplus/llar/cmd

go 1.25.0

require (
	github.com/goplus/llar v0.3.5
	github.com/goplus/llar/build v0.0.0-00010101000000-000000000000
	github.com/jessevdk/go-flags v1.6.1
	github.com/qiniu/x v1.18.3
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/goplus/gogen v1.23.6-0.20260816085451-b240c8b2c5b1 // indirect
	github.com/goplus/ixgo v1.1.6 // indirect
	github.com/goplus/mod v0.21.2 // indirect
	github.com/goplus/reflectx v1.7.8 // indirect
	github.com/goplus/xgo v1.7.6-0.20260818050008-fd36b6192e30 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/petermattis/goid v0.0.0-20260713073324-57ed88f34a0a // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/visualfc/funcval v0.1.5 // indirect
	github.com/visualfc/gid v0.3.5 // indirect
	github.com/visualfc/goembed v0.3.4 // indirect
	github.com/visualfc/xtype v0.3.3 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
)

replace (
	github.com/goplus/llar => ..
	github.com/goplus/llar/build => ../build
)
