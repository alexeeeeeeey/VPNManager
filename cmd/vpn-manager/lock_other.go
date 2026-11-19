//go:build !linux

package main

func lockInstance(string) (func(), error) { return func() {}, nil }
