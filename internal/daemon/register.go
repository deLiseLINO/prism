package daemon

import (
	"context"
	"net"
	"os"
	"strconv"

	"github.com/deLiseLINO/prism/internal/buildinfo"
	"github.com/deLiseLINO/prism/internal/service"
)

func registerSelf(ctx context.Context, stop context.CancelFunc, id string, addr net.Addr) (func(), error) {
	stateDir := service.StateDir()
	reg := service.Registration{ID: id, Version: buildinfo.Version, URL: listenURL(addr), PID: os.Getpid()}
	if err := service.WriteRegistration(stateDir, reg); err != nil {
		return nil, err
	}
	go service.Watch(ctx, stateDir, id, service.WatchInterval, stop)
	return func() { _ = service.RemoveOwnRegistration(stateDir, id) }, nil
}

func listenURL(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return "http://" + addr.String()
	}
	ip := tcp.IP
	if ip == nil || ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(tcp.Port))
}
