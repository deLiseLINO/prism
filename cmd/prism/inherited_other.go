//go:build !linux

package main

func closeInheritedDescriptors() error { return nil }
