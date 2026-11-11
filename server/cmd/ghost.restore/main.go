// ghost.restore , the OFFLINE half of the backup system. Two subcommands:
//
//	ghost.restore keygen
//	    Generates an X25519 keypair. Prints the PRIVATE key hex to stdout (write it down, store
//	    it away from the box , it is the only thing that can ever read a backup) and writes the
//	    public half to ./backup.pub for copying to /var/lib/ghost/backup.pub on the box.
//
//	ghost.restore unseal <file.tar.seal> out.tar < private-key-hex
//	    Reads the private key hex from stdin (never argv , argv is world-readable in /proc),
//	    decrypts the sealed backup and writes out.tar only when the whole file authenticated: a
//	    backup that was cut short or changed leaves nothing. Then `tar -x -C <mount> -f out.tar`.
//	    Without out.tar the tar goes to stdout for a pipe; check the exit status, tar will have
//	    extracted whatever came before a fault.
//
// The box never runs this. The box holds only backup.pub and can seal but never unseal , that is
// the entire point.
package main

import (
	"bufio"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/framed"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: ghost.restore keygen | ghost.restore unseal <file.tar.seal> [out.tar]")
	}
	switch os.Args[1] {
	case "keygen":
		keygen()
	case "unseal":
		if len(os.Args) < 3 {
			fatal("unseal requires the sealed file path")
		}
		out := ""
		if len(os.Args) > 3 {
			out = os.Args[3]
		}
		unseal(os.Args[2], out)
	default:
		fatal("unknown subcommand " + os.Args[1])
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "ghost.restore: "+msg)
	os.Exit(1)
}

func keygen() {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		fatal("keygen: " + err.Error())
	}
	pubHex := hex.EncodeToString(priv.PublicKey().Bytes())
	if err := os.WriteFile("backup.pub", []byte(pubHex+"\n"), 0o600); err != nil {
		fatal("write backup.pub: " + err.Error())
	}
	fmt.Fprintln(os.Stderr, "public key written to ./backup.pub , copy it to /var/lib/ghost/backup.pub on the box")
	fmt.Fprintln(os.Stderr, "PRIVATE key follows on stdout , store it OFFLINE, it is the only thing that can read a backup:")
	fmt.Println(hex.EncodeToString(priv.Bytes()))
}

// unseal opens a sealed backup. With an output file the tar is written beside it under a
// temporary name and takes its real name only after the last chunk has authenticated, so a
// backup that is cut short or changed leaves nothing to extract. Without one the tar goes to
// stdout as it is read, for a pipe: the exit status is then the only word on whether it was
// whole, and tar will already have extracted what came before the fault.
func unseal(path, outPath string) {
	keyHex, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		fatal("read private key from stdin: " + err.Error())
	}
	privBytes, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(privBytes) != 32 {
		fatal("private key must be 64 hex chars on stdin")
	}
	f, err := os.Open(path)
	if err != nil {
		fatal(err.Error())
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	if outPath == "" {
		out := bufio.NewWriterSize(os.Stdout, 1<<20)
		if err := framed.Unseal(r, out, privBytes); err != nil {
			_ = out.Flush()
			fatal(err.Error() + " , what was written before this is NOT a whole backup")
		}
		if err := out.Flush(); err != nil {
			fatal("write: " + err.Error() + " , the output is NOT a whole backup")
		}
		return
	}
	tmp := outPath + ".part"
	of, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fatal(err.Error())
	}
	out := bufio.NewWriterSize(of, 1<<20)
	err = framed.Unseal(r, out, privBytes)
	if err == nil {
		err = out.Flush()
	}
	if err == nil {
		err = of.Sync()
	}
	if cerr := of.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, outPath)
	}
	if err != nil {
		_ = os.Remove(tmp)
		fatal(err.Error() + " , nothing was written")
	}
	fmt.Fprintln(os.Stderr, "whole and authentic: "+outPath)
}
