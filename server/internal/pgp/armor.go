package pgp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
)

// ASCII armor (RFC 4880 section 6): a block type, base64 of the packets in 64-column lines, and
// a CRC-24 line. The box armors what it sends inside mail and reads whatever armor comes.

const (
	BlockMessage   = "PGP MESSAGE"
	BlockPublicKey = "PGP PUBLIC KEY BLOCK"
	BlockSignature = "PGP SIGNATURE"
)

const crc24Init, crc24Poly = 0xB704CE, 0x1864CFB

func crc24(b []byte) uint32 {
	crc := uint32(crc24Init)
	for _, c := range b {
		crc ^= uint32(c) << 16
		for i := 0; i < 8; i++ {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= crc24Poly
			}
		}
	}
	return crc & 0xFFFFFF
}

// Armor wraps binary packets in a block of the given type.
func Armor(blockType string, data []byte) string {
	var b strings.Builder
	b.WriteString("-----BEGIN " + blockType + "-----\n\n")
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 64 {
		b.WriteString(enc[:64] + "\n")
		enc = enc[64:]
	}
	b.WriteString(enc + "\n")
	crc := crc24(data)
	b.WriteString("=" + base64.StdEncoding.EncodeToString([]byte{byte(crc >> 16), byte(crc >> 8), byte(crc)}) + "\n")
	b.WriteString("-----END " + blockType + "-----\n")
	return b.String()
}

// Dearmor reads the first armored block in s. Header lines (Version, Comment) are skipped; the
// CRC line is checked when present (RFC 9580 made it optional).
func Dearmor(s string) (blockType string, data []byte, err error) {
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	state := 0 // 0 before, 1 headers, 2 body
	var b64 strings.Builder
	var crcLine string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		switch state {
		case 0:
			if strings.HasPrefix(line, "-----BEGIN ") && strings.HasSuffix(line, "-----") {
				blockType = strings.TrimSuffix(strings.TrimPrefix(line, "-----BEGIN "), "-----")
				state = 1
			}
		case 1:
			if line == "" {
				state = 2
			} else if !strings.Contains(line, ": ") {
				// no headers at all: this is the first body line
				state = 2
				b64.WriteString(line)
			}
		case 2:
			switch {
			case strings.HasPrefix(line, "-----END "):
				data, err = base64.StdEncoding.DecodeString(b64.String())
				if err != nil {
					return "", nil, fmt.Errorf("armor: %v", err)
				}
				if crcLine != "" {
					c, err := base64.StdEncoding.DecodeString(crcLine)
					if err != nil || len(c) != 3 {
						return "", nil, fmt.Errorf("armor: bad checksum line")
					}
					if crc24(data) != uint32(c[0])<<16|uint32(c[1])<<8|uint32(c[2]) {
						return "", nil, fmt.Errorf("armor: checksum mismatch")
					}
				}
				return blockType, data, nil
			case strings.HasPrefix(line, "="):
				crcLine = line[1:]
			default:
				b64.WriteString(line)
			}
		}
	}
	if state == 0 {
		return "", nil, fmt.Errorf("armor: no block found")
	}
	return "", nil, fmt.Errorf("armor: unterminated block")
}

// unwrap takes armored or binary input and returns the packets' bytes.
func unwrap(data []byte) ([]byte, error) {
	if bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("-----BEGIN ")) {
		_, b, err := Dearmor(string(data))
		return b, err
	}
	return data, nil
}
