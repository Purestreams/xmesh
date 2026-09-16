package gateway

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"xmesh/internal/identity"
	"xmesh/internal/model"
	"xmesh/internal/protocol"
	"xmesh/internal/relay"
)

const (
	socksVersion             = 5
	socksAuthUserPassword    = 2
	socksCommandConnect      = 1
	socksCommandUDPAssociate = 3
)

type socksTarget struct {
	Host string
	Port int
}

func (r *Runtime) runSOCKS(ctx context.Context) error {
	listener, err := net.Listen("tcp", r.local.Gateway.SOCKSListen)
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() { <-ctx.Done(); _ = listener.Close() }()
	r.logger.Info("gateway SOCKS listener started", "address", listener.Addr())
	sem := make(chan struct{}, r.local.Gateway.MaxUDPAssociations)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go r.handleSOCKS(conn, sem)
	}
}

func (r *Runtime) handleSOCKS(conn net.Conn, udpSem chan struct{}) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReaderSize(conn, 4096)
	username, password, err := socksAuthenticate(reader, conn)
	if err != nil {
		return
	}
	grant, agentID, ok := r.grant(username, password)
	if !ok {
		return
	}
	command, target, err := readSOCKSRequest(reader)
	if err != nil {
		_ = writeSOCKSReply(conn, 1, nil)
		return
	}
	switch command {
	case socksCommandConnect:
		requestID, _ := identity.Token(12)
		stream, lease, err := r.openStream(agentID, protocol.Message{Type: protocol.TypeTCP, Version: protocol.Version, GrantID: grant.ID, RequestID: requestID, Host: target.Host, Port: target.Port})
		if err != nil {
			_ = writeSOCKSReply(conn, mapSOCKSError(err), nil)
			return
		}
		defer lease.Release()
		r.tcpConnections.Add(1)
		defer r.tcpConnections.Add(-1)
		r.updateGrant(grant.ID, func(status *model.GrantStatus) { status.TCPConnections++ })
		defer r.updateGrant(grant.ID, func(status *model.GrantStatus) { status.TCPConnections-- })
		if err := writeSOCKSReply(conn, 0, &net.TCPAddr{IP: net.IPv4zero, Port: 0}); err != nil {
			stream.Close()
			return
		}
		_ = conn.SetDeadline(time.Time{})
		relay.Bidirectional(conn, stream, func(n int) {
			r.updateGrant(grant.ID, func(status *model.GrantStatus) { status.UploadBytes += uint64(n) })
		}, func(n int) {
			r.updateGrant(grant.ID, func(status *model.GrantStatus) { status.DownloadBytes += uint64(n) })
		})
	case socksCommandUDPAssociate:
		select {
		case udpSem <- struct{}{}:
			defer func() { <-udpSem }()
		default:
			_ = writeSOCKSReply(conn, 1, nil)
			return
		}
		r.udpAssociations.Add(1)
		defer r.udpAssociations.Add(-1)
		r.updateGrant(grant.ID, func(status *model.GrantStatus) { status.UDPAssociations++ })
		defer r.updateGrant(grant.ID, func(status *model.GrantStatus) { status.UDPAssociations-- })
		r.handleUDPAssociation(conn, reader, grant.ID, agentID)
	default:
		_ = writeSOCKSReply(conn, 7, nil)
	}
}

func socksAuthenticate(reader *bufio.Reader, conn net.Conn) (string, string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", "", err
	}
	if header[0] != socksVersion || header[1] == 0 {
		return "", "", errors.New("invalid SOCKS greeting")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return "", "", err
	}
	supported := false
	for _, method := range methods {
		if method == socksAuthUserPassword {
			supported = true
		}
	}
	if !supported {
		_, _ = conn.Write([]byte{socksVersion, 0xff})
		return "", "", errors.New("authentication required")
	}
	if _, err := conn.Write([]byte{socksVersion, socksAuthUserPassword}); err != nil {
		return "", "", err
	}
	version, err := reader.ReadByte()
	if err != nil || version != 1 {
		return "", "", errors.New("invalid username/password auth")
	}
	username, err := readLengthString(reader)
	if err != nil {
		return "", "", err
	}
	password, err := readLengthString(reader)
	if err != nil {
		return "", "", err
	}
	if username == "" || password == "" {
		_, _ = conn.Write([]byte{1, 1})
		return "", "", errors.New("empty credentials")
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return "", "", err
	}
	return username, password, nil
}

func readLengthString(reader *bufio.Reader) (string, error) {
	n, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(reader, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readSOCKSRequest(reader *bufio.Reader) (byte, socksTarget, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, socksTarget{}, err
	}
	if header[0] != socksVersion || header[2] != 0 {
		return 0, socksTarget{}, errors.New("invalid SOCKS request")
	}
	target, _, err := readSOCKSAddress(reader, header[3])
	return header[1], target, err
}

func readSOCKSAddress(reader io.Reader, addressType byte) (socksTarget, int, error) {
	var host string
	consumed := 0
	switch addressType {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(reader, b); err != nil {
			return socksTarget{}, 0, err
		}
		host = net.IP(b).String()
		consumed = 4
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return socksTarget{}, 0, err
		}
		b := make([]byte, int(size[0]))
		if _, err := io.ReadFull(reader, b); err != nil {
			return socksTarget{}, 0, err
		}
		host = string(b)
		consumed = 1 + len(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(reader, b); err != nil {
			return socksTarget{}, 0, err
		}
		host = net.IP(b).String()
		consumed = 16
	default:
		return socksTarget{}, 0, errors.New("unsupported address type")
	}
	var port [2]byte
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return socksTarget{}, 0, err
	}
	return socksTarget{Host: host, Port: int(binary.BigEndian.Uint16(port[:]))}, consumed + 2, nil
}

func writeSOCKSReply(w io.Writer, reply byte, address net.Addr) error {
	ip := net.IPv4zero
	port := 0
	if tcp, ok := address.(*net.TCPAddr); ok {
		ip = tcp.IP
		port = tcp.Port
	}
	if udp, ok := address.(*net.UDPAddr); ok {
		ip = udp.IP
		port = udp.Port
	}
	if v4 := ip.To4(); v4 != nil {
		b := []byte{socksVersion, reply, 0, 1}
		b = append(b, v4...)
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], uint16(port))
		b = append(b, p[:]...)
		_, err := w.Write(b)
		return err
	}
	b := []byte{socksVersion, reply, 0, 4}
	b = append(b, ip.To16()...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(port))
	b = append(b, p[:]...)
	_, err := w.Write(b)
	return err
}

func mapSOCKSError(err error) byte {
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return 5
	}
	return 1
}

func (r *Runtime) handleUDPAssociation(control net.Conn, reader *bufio.Reader, grantID, agentID string) {
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		_ = writeSOCKSReply(control, 1, nil)
		return
	}
	defer udp.Close()
	if err := writeSOCKSReply(control, 0, udp.LocalAddr()); err != nil {
		return
	}
	_ = control.SetDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { one := make([]byte, 1); _, _ = reader.Read(one); cancel(); _ = udp.Close() }()
	queue := make(chan protocol.Datagram, 64)
	var client *net.UDPAddr
	go func() {
		buffer := make([]byte, 65535)
		for {
			_ = udp.SetReadDeadline(time.Now().Add(r.local.Gateway.UDPIdleTimeout.Value(2 * time.Minute)))
			n, source, err := udp.ReadFromUDP(buffer)
			if err != nil {
				cancel()
				return
			}
			if client == nil {
				client = source
			} else if client.String() != source.String() {
				continue
			}
			datagram, err := decodeSOCKSUDP(buffer[:n])
			if err != nil {
				continue
			}
			select {
			case queue <- datagram:
			default:
				r.logger.Warn("gateway UDP queue full; dropping datagram", "grant", grantID, "bytes", len(datagram.Payload))
			}
		}
	}()
	var first protocol.Datagram
	select {
	case <-ctx.Done():
		return
	case first = <-queue:
	}
	requestID, _ := identity.Token(12)
	stream, lease, err := r.openStream(agentID, protocol.Message{Type: protocol.TypeUDP, Version: protocol.Version, GrantID: grantID, RequestID: requestID})
	if err != nil {
		r.logger.Warn("gateway UDP stream open failed", "grant", grantID, "error", err)
		return
	}
	defer stream.Close()
	defer lease.Release()
	errCh := make(chan error, 2)
	go func() {
		datagram := first
		for {
			if err := protocol.WriteDatagram(stream, datagram); err != nil {
				errCh <- err
				return
			}
			r.updateGrant(grantID, func(status *model.GrantStatus) { status.UploadBytes += uint64(len(datagram.Payload)) })
			select {
			case <-ctx.Done():
				return
			case datagram = <-queue:
			}
		}
	}()
	go func() {
		for {
			reply, err := protocol.ReadDatagram(stream)
			if err != nil {
				errCh <- err
				return
			}
			packet, err := encodeSOCKSUDP(reply)
			if err != nil {
				continue
			}
			if _, err := udp.WriteToUDP(packet, client); err != nil {
				errCh <- err
				return
			}
			r.updateGrant(grantID, func(status *model.GrantStatus) { status.DownloadBytes += uint64(len(reply.Payload)) })
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
			r.logger.Warn("gateway UDP tunnel ended", "grant", grantID, "error", err)
		}
	}
}

func decodeSOCKSUDP(packet []byte) (protocol.Datagram, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return protocol.Datagram{}, errors.New("invalid SOCKS UDP header")
	}
	reader := bytesReader{b: packet[4:]}
	target, consumed, err := readSOCKSAddress(&reader, packet[3])
	if err != nil {
		return protocol.Datagram{}, err
	}
	offset := 4 + consumed
	if offset > len(packet) {
		return protocol.Datagram{}, io.ErrUnexpectedEOF
	}
	payload := append([]byte(nil), packet[offset:]...)
	return protocol.Datagram{Host: target.Host, Port: target.Port, Payload: payload}, nil
}

func encodeSOCKSUDP(datagram protocol.Datagram) ([]byte, error) {
	host := net.ParseIP(datagram.Host)
	var b []byte
	b = append(b, 0, 0, 0)
	if host != nil {
		if v4 := host.To4(); v4 != nil {
			b = append(b, 1)
			b = append(b, v4...)
		} else {
			b = append(b, 4)
			b = append(b, host.To16()...)
		}
	} else {
		if len(datagram.Host) > 255 {
			return nil, errors.New("domain too long")
		}
		b = append(b, 3, byte(len(datagram.Host)))
		b = append(b, datagram.Host...)
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(datagram.Port))
	b = append(b, port[:]...)
	b = append(b, datagram.Payload...)
	return b, nil
}

type bytesReader struct{ b []byte }

func (r *bytesReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
