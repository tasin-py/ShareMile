package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strings"
)

// A network code carries the secret key plus every address the host can be
// reached on, so joining needs no central server:
//   SM-XXXXX-XXXXX-...  (Crockford base32 of: ver | key[10] | endpoints...)
// endpoint: 4 ip4[4] port[2] | 6 ip6[16] port[2] | 'h' len host[len]

const b32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func b32enc(b []byte) string {
	var sb strings.Builder
	var buf uint32
	bits := 0
	for _, c := range b {
		buf = buf<<8 | uint32(c)
		bits += 8
		for bits >= 5 {
			sb.WriteByte(b32[(buf>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		sb.WriteByte(b32[(buf<<(5-bits))&31])
	}
	return sb.String()
}

func b32dec(s string) ([]byte, error) {
	var out []byte
	var buf uint32
	bits := 0
	for _, r := range strings.ToUpper(s) {
		switch r {
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		i := strings.IndexRune(b32, r)
		if i < 0 {
			return nil, errors.New("invalid character in code")
		}
		buf = buf<<5 | uint32(i)
		bits += 5
		if bits >= 8 {
			out = append(out, byte(buf>>(bits-8)))
			bits -= 8
		}
	}
	return out, nil
}

type NetCode struct {
	Key       []byte
	Endpoints []string // "ip:port" or "https://host"
}

func netID(key []byte) string {
	h := sha256.Sum256(append([]byte("sharemile-net:"), key...))
	return hex.EncodeToString(h[:8])
}

func authToken(key []byte) string {
	h := sha256.Sum256(append([]byte("sharemile-auth:"), key...))
	return hex.EncodeToString(h[:16])
}

func encodeCode(c NetCode) string {
	b := []byte{1}
	b = append(b, c.Key...)
	for _, e := range c.Endpoints {
		if strings.HasPrefix(e, "https://") {
			h := strings.TrimPrefix(e, "https://")
			if sub, ok := strings.CutSuffix(h, ".trycloudflare.com"); ok && len(sub) < 250 && strings.Trim(sub, "abcdefghijklmnopqrstuvwxyz-") == "" {
				b = append(b, 't', byte(len(sub)))
				b = append(b, packWords(sub)...)
				continue
			}
			if len(h) < 250 {
				b = append(b, 'h', byte(len(h)))
				b = append(b, h...)
			}
			continue
		}
		host, ps, err := net.SplitHostPort(e)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		var port uint16
		for _, ch := range ps {
			port = port*10 + uint16(ch-'0')
		}
		pb := make([]byte, 2)
		binary.BigEndian.PutUint16(pb, port)
		if ip4 := ip.To4(); ip4 != nil {
			b = append(b, 4)
			b = append(b, ip4...)
		} else if ip != nil {
			b = append(b, 6)
			b = append(b, ip.To16()...)
		} else {
			continue
		}
		b = append(b, pb...)
	}
	s := b32enc(b)
	var sb strings.Builder
	sb.WriteString("SM")
	for i := 0; i < len(s); i += 5 {
		sb.WriteByte('-')
		end := i + 5
		if end > len(s) {
			end = len(s)
		}
		sb.WriteString(s[i:end])
	}
	return sb.String()
}

func decodeCode(code string) (NetCode, error) {
	var c NetCode
	s := strings.ToUpper(strings.TrimSpace(code))
	s = strings.TrimPrefix(s, "SM")
	s = strings.NewReplacer("-", "", " ", "", "\n", "", "\r", "", "\t", "").Replace(s)
	b, err := b32dec(s)
	if err != nil {
		return c, err
	}
	if len(b) < 11 || b[0] != 1 {
		return c, errors.New("this doesn't look like a ShareMile network code")
	}
	c.Key = append([]byte(nil), b[1:11]...)
	b = b[11:]
	for len(b) > 0 {
		switch b[0] {
		case 4:
			if len(b) < 7 {
				return c, nil
			}
			c.Endpoints = append(c.Endpoints, net.JoinHostPort(net.IP(b[1:5]).String(), itoa(int(binary.BigEndian.Uint16(b[5:7])))))
			b = b[7:]
		case 6:
			if len(b) < 19 {
				return c, nil
			}
			c.Endpoints = append(c.Endpoints, net.JoinHostPort(net.IP(b[1:17]).String(), itoa(int(binary.BigEndian.Uint16(b[17:19])))))
			b = b[19:]
		case 't':
			if len(b) < 2 {
				return c, nil
			}
			n := int(b[1])
			nb := (n*5 + 7) / 8
			if len(b) < 2+nb {
				return c, nil
			}
			c.Endpoints = append(c.Endpoints, "https://"+unpackWords(b[2:2+nb], n)+".trycloudflare.com")
			b = b[2+nb:]
		case 'h':
			if len(b) < 2 || len(b) < 2+int(b[1]) {
				return c, nil
			}
			c.Endpoints = append(c.Endpoints, "https://"+string(b[2:2+int(b[1])]))
			b = b[2+int(b[1]):]
		default:
			return c, nil // trailing padding bits
		}
	}
	return c, nil
}

const wordAlpha = "abcdefghijklmnopqrstuvwxyz-"

// packWords stores a relay subdomain (a-z and '-') at 5 bits per character.
func packWords(s string) []byte {
	var out []byte
	var buf uint32
	bits := 0
	for _, r := range s {
		buf = buf<<5 | uint32(strings.IndexRune(wordAlpha, r))
		bits += 5
		for bits >= 8 {
			out = append(out, byte(buf>>(bits-8)))
			bits -= 8
		}
	}
	if bits > 0 {
		out = append(out, byte(buf<<(8-bits)))
	}
	return out
}

func unpackWords(b []byte, n int) string {
	var sb strings.Builder
	var buf uint32
	bits := 0
	for _, c := range b {
		buf = buf<<8 | uint32(c)
		bits += 8
		for bits >= 5 && sb.Len() < n {
			i := (buf >> (bits - 5)) & 31
			bits -= 5
			if int(i) < len(wordAlpha) {
				sb.WriteByte(wordAlpha[i])
			}
		}
	}
	return sb.String()
}

func endpointURL(e string) string {
	if strings.HasPrefix(e, "https://") || strings.HasPrefix(e, "http://") {
		return e
	}
	return "http://" + e
}
