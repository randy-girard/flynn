package main

import (
    "crypto/sha512"
    "encoding/hex"
    "io"
    "log"
    "os"
)

func main() {
    if len(os.Args) != 2 {
        log.Fatal("Usage: sha512_256 <file>")
    }
    f, err := os.Open(os.Args[1])
    if err != nil { log.Fatal(err) }
    defer f.Close()
    h := sha512.New512_256()
    if _, err := io.Copy(h, f); err != nil { log.Fatal(err) }
    os.Stdout.WriteString(hex.EncodeToString(h.Sum(nil)) + "\n")
}
