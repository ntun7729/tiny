package main

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
)

const (
	commandTCP = 1
	commandUDP = 2
)

type vlessRequest struct {
	command byte
	address string
	port    uint16
	payload []byte
}

func parseVLESSRequest(message []byte, expectedUUID [16]byte) (vlessRequest, error) {
	if len(message) < 22 {
		return vlessRequest{}, errors.New("VLESS request is too short")
	}
	if message[0] != 0 {
		return vlessRequest{}, errors.New("unsupported VLESS version")
	}
	if subtle.ConstantTimeCompare(message[1:17], expectedUUID[:]) != 1 {
		return vlessRequest{}, errors.New("authentication failed")
	}

	commandIndex := 18 + int(message[17])
	if commandIndex+4 > len(message) {
		return vlessRequest{}, errors.New("invalid VLESS addon length")
	}

	command := message[commandIndex]
	if command != commandTCP && command != commandUDP {
		return vlessRequest{}, errors.New("unsupported VLESS command")
	}

	port := binary.BigEndian.Uint16(message[commandIndex+1 : commandIndex+3])
	if port == 0 {
		return vlessRequest{}, errors.New("destination port must not be zero")
	}

	addressTypeIndex := commandIndex + 3
	addressType := message[addressTypeIndex]
	cursor := addressTypeIndex + 1

	var address string
	switch addressType {
	case 1:
		if cursor+4 > len(message) {
			return vlessRequest{}, errors.New("truncated IPv4 address")
		}
		address = net.IP(message[cursor : cursor+4]).String()
		cursor += 4
	case 2:
		if cursor >= len(message) {
			return vlessRequest{}, errors.New("missing domain length")
		}
		domainLength := int(message[cursor])
		cursor++
		if domainLength == 0 || cursor+domainLength > len(message) {
			return vlessRequest{}, errors.New("invalid domain name")
		}
		address = string(message[cursor : cursor+domainLength])
		cursor += domainLength
	case 3:
		if cursor+16 > len(message) {
			return vlessRequest{}, errors.New("truncated IPv6 address")
		}
		address = net.IP(message[cursor : cursor+16]).String()
		cursor += 16
	default:
		return vlessRequest{}, errors.New("unsupported address type")
	}

	return vlessRequest{
		command: command,
		address: address,
		port:    port,
		payload: message[cursor:],
	}, nil
}

func readVLESSRequest(reader io.Reader, expectedUUID [16]byte) (vlessRequest, error) {
	fixed := make([]byte, 18)
	if _, err := io.ReadFull(reader, fixed); err != nil {
		return vlessRequest{}, err
	}

	addonLength := int(fixed[17])
	tail := make([]byte, addonLength+4)
	if _, err := io.ReadFull(reader, tail); err != nil {
		return vlessRequest{}, err
	}

	message := make([]byte, 0, len(fixed)+len(tail)+17)
	message = append(message, fixed...)
	message = append(message, tail...)

	addressType := tail[addonLength+3]
	switch addressType {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(reader, address); err != nil {
			return vlessRequest{}, err
		}
		message = append(message, address...)
	case 2:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return vlessRequest{}, err
		}
		message = append(message, length[0])
		domain := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, domain); err != nil {
			return vlessRequest{}, err
		}
		message = append(message, domain...)
	case 3:
		address := make([]byte, 16)
		if _, err := io.ReadFull(reader, address); err != nil {
			return vlessRequest{}, err
		}
		message = append(message, address...)
	default:
		return vlessRequest{}, errors.New("unsupported address type")
	}

	return parseVLESSRequest(message, expectedUUID)
}
