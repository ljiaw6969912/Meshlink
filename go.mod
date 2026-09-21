module meshlink

go 1.26

// Local Walk patch fixes asynchronous layout shutdown; see third_party/walk/README.meshlink.md.
replace github.com/lxn/walk => ./third_party/walk

require (
	github.com/lxn/walk v0.0.0-20210112085537-c389da54e794
	github.com/quic-go/quic-go v0.61.0
	golang.org/x/crypto v0.54.0
	golang.org/x/sys v0.47.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
)

require (
	github.com/lxn/win v0.0.0-20210218163916-a377121e959e // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gopkg.in/Knetic/govaluate.v3 v3.0.0 // indirect
)
