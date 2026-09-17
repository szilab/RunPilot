package web

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/szilab/RunPilot/internal/remote/guacd"
)

type recordingGuacamoleOutbound struct {
	mu       sync.Mutex
	messages [][]byte
}

func (w *recordingGuacamoleOutbound) Write(_ context.Context, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.messages = append(w.messages, append([]byte(nil), data...))
	return nil
}

func (w *recordingGuacamoleOutbound) snapshot() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	messages := make([][]byte, len(w.messages))
	for i, message := range w.messages {
		messages[i] = append([]byte(nil), message...)
	}
	return messages
}

func encodeGuacdInstruction(t *testing.T, opcode string, args ...string) []byte {
	t.Helper()
	data, err := guacd.EncodeInstruction(opcode, args...)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeMessage(t *testing.T, data []byte) guacd.Instruction {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(data))
	instruction, err := guacd.DecodeInstruction(reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Peek(1); err == nil {
		t.Fatalf("message contains more than one instruction: %q", data)
	}
	return instruction
}

func TestForwardGuacdInstructionsDoesNotInterleaveFragmentedOutputAndPing(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	output := &recordingGuacamoleOutbound{}
	done := make(chan error, 1)
	go func() {
		done <- forwardGuacdInstructions(context.Background(), bufio.NewReader(client), output, nil)
	}()

	large := encodeGuacdInstruction(t, "blob", "stream", strings.Repeat("x", 32*1024))
	if _, err := server.Write(large[:32]); err != nil {
		t.Fatal(err)
	}
	ping, err := guacd.TunnelKeepalive()
	if err != nil {
		t.Fatal(err)
	}
	responded, err := forwardBrowserInstructions(context.Background(), ping, nil, output)
	if err != nil || !responded {
		t.Fatalf("responded=%v err=%v", responded, err)
	}
	if _, err := server.Write(large[32:]); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	messages := output.snapshot()
	if len(messages) != 2 {
		t.Fatalf("messages=%d", len(messages))
	}
	first, second := decodeMessage(t, messages[0]), decodeMessage(t, messages[1])
	if first.Opcode != "" || !reflect.DeepEqual(first.Args, []string{"ping", "server"}) {
		t.Fatalf("ping response=%#v", first)
	}
	if second.Opcode != "blob" || len(second.Args) != 2 || second.Args[1] != strings.Repeat("x", 32*1024) {
		t.Fatalf("fragmented instruction=%#v", second)
	}
}

func TestForwardGuacdInstructionsPreservesNormalDisplayInstructionOrder(t *testing.T) {
	stream := append(encodeGuacdInstruction(t, "size", "1280", "800", "96"), encodeGuacdInstruction(t, "rect", "0", "0", "1280", "800")...)
	stream = append(stream, encodeGuacdInstruction(t, "png", "0", "0", "0")...)
	stream = append(stream, encodeGuacdInstruction(t, "sync", "1234")...)
	output := &recordingGuacamoleOutbound{}
	var diagnostics []string
	err := forwardGuacdInstructions(context.Background(), bufio.NewReader(bytes.NewReader(stream)), output, guacdInstructionDiagnostics(func(line string) {
		diagnostics = append(diagnostics, line)
	}))
	if err != nil {
		t.Fatal(err)
	}
	var opcodes []string
	for _, message := range output.snapshot() {
		opcodes = append(opcodes, decodeMessage(t, message).Opcode)
	}
	if !reflect.DeepEqual(opcodes, []string{"size", "rect", "png", "sync"}) {
		t.Fatalf("opcodes=%q", opcodes)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%q", diagnostics)
	}
}

func TestForwardGuacdInstructionsPropagatesErrorInstructionAndDiagnostic(t *testing.T) {
	errorInstruction := encodeGuacdInstruction(t, "error", "RDP connection failed", "514")
	output := &recordingGuacamoleOutbound{}
	var diagnostics []string
	err := forwardGuacdInstructions(context.Background(), bufio.NewReader(bytes.NewReader(errorInstruction)), output, guacdInstructionDiagnostics(func(line string) {
		diagnostics = append(diagnostics, line)
	}))
	if err != nil {
		t.Fatal(err)
	}
	messages := output.snapshot()
	if len(messages) != 1 || !bytes.Equal(messages[0], errorInstruction) {
		t.Fatalf("error frame was not preserved: %q", messages)
	}
	if !reflect.DeepEqual(diagnostics, []string{"guacd -> error (code=514)"}) {
		t.Fatalf("diagnostics=%q", diagnostics)
	}
}

func TestForwardGuacdInstructionsPreservesFirstImageStream(t *testing.T) {
	const payload = "sensitive-image-payload-must-not-be-logged"
	stream := append(encodeGuacdInstruction(t, "img", "255", "7", "3", "image/png", "12", "24"), encodeGuacdInstruction(t, "blob", "7", payload)...)
	stream = append(stream, encodeGuacdInstruction(t, "blob", "7", "more-data")...)
	stream = append(stream, encodeGuacdInstruction(t, "end", "7")...)
	stream = append(stream, encodeGuacdInstruction(t, "sync", "9876")...)

	output := &recordingGuacamoleOutbound{}
	var diagnostics []string
	if err := forwardGuacdInstructions(context.Background(), bufio.NewReader(bytes.NewReader(stream)), output, guacdInstructionDiagnostics(func(line string) {
		diagnostics = append(diagnostics, line)
	})); err != nil {
		t.Fatal(err)
	}

	var opcodes []string
	for _, message := range output.snapshot() {
		opcodes = append(opcodes, decodeMessage(t, message).Opcode)
	}
	if !reflect.DeepEqual(opcodes, []string{"img", "blob", "blob", "end", "sync"}) {
		t.Fatalf("opcodes=%q", opcodes)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("normal image flow should not create diagnostics: %q", diagnostics)
	}
}

func TestForwardBrowserInstructionsImmediatelyForwardsRuntimeInstructions(t *testing.T) {
	client, fakeGuacd := net.Pipe()
	defer client.Close()
	defer fakeGuacd.Close()
	input := append(encodeGuacdInstruction(t, "sync", "12345"), encodeGuacdInstruction(t, "size", "1280", "800", "96")...)
	input = append(input, encodeGuacdInstruction(t, "nop")...)
	input = append(input, encodeGuacdInstruction(t, "mouse", "10", "20", "0")...)
	done := make(chan error, 1)
	go func() {
		_, err := forwardBrowserInstructions(context.Background(), input, client, &recordingGuacamoleOutbound{})
		done <- err
	}()

	if err := fakeGuacd.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(fakeGuacd)
	for _, want := range []guacd.Instruction{
		{Opcode: "sync", Args: []string{"12345"}},
		{Opcode: "size", Args: []string{"1280", "800", "96"}},
		{Opcode: "nop", Args: []string{}},
		{Opcode: "mouse", Args: []string{"10", "20", "0"}},
	} {
		if got, err := guacd.DecodeInstruction(reader); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("instruction=%#v err=%v want=%#v", got, err, want)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSyncRoundTripAllowsImageStreamProgression(t *testing.T) {
	client, fakeGuacd := net.Pipe()
	defer client.Close()
	defer fakeGuacd.Close()
	output := &recordingGuacamoleOutbound{}
	guacdDone := make(chan error, 1)
	go func() {
		guacdDone <- forwardGuacdInstructions(context.Background(), bufio.NewReader(client), output, nil)
	}()

	if _, err := fakeGuacd.Write(encodeGuacdInstruction(t, "sync", "100")); err != nil {
		t.Fatal(err)
	}
	var diagnostics []string
	browserDone := make(chan error, 1)
	go func() {
		_, err := forwardBrowserInstructionsWithObserver(context.Background(), encodeGuacdInstruction(t, "sync", "100"), client, output, browserInstructionDiagnostics(func(line string) {
			diagnostics = append(diagnostics, line)
		}))
		browserDone <- err
	}()
	if err := fakeGuacd.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, err := guacd.DecodeInstruction(bufio.NewReader(fakeGuacd)); err != nil || got.Opcode != "sync" || !reflect.DeepEqual(got.Args, []string{"100"}) {
		t.Fatalf("forwarded sync=%#v err=%v", got, err)
	}
	if err := <-browserDone; err != nil {
		t.Fatal(err)
	}
	for _, want := range []guacd.Instruction{
		{Opcode: "img", Args: []string{"255", "14", "-4", "image/png", "0", "0"}},
		{Opcode: "blob", Args: []string{"14", "image-data"}},
		{Opcode: "end", Args: []string{"14"}},
		{Opcode: "sync", Args: []string{"101"}},
	} {
		if _, err := fakeGuacd.Write(encodeGuacdInstruction(t, want.Opcode, want.Args...)); err != nil {
			t.Fatal(err)
		}
	}
	_ = fakeGuacd.Close()
	if err := <-guacdDone; err != nil {
		t.Fatal(err)
	}
	var opcodes []string
	for _, message := range output.snapshot() {
		opcodes = append(opcodes, decodeMessage(t, message).Opcode)
	}
	if !reflect.DeepEqual(opcodes, []string{"sync", "img", "blob", "end", "sync"}) {
		t.Fatalf("browser instruction order=%q", opcodes)
	}
	for _, want := range []string{"browser -> sync received timestamp=100", "browser -> sync forwarded timestamp=100"} {
		if !containsDiagnostic(diagnostics, want) {
			t.Fatalf("missing %q in diagnostics=%q", want, diagnostics)
		}
	}
}

func TestBrowserResizeDiagnosticsRecordsReceiptAndForwardingOnly(t *testing.T) {
	var diagnostics []string
	observe := browserResizeDiagnostics(func(line string) { diagnostics = append(diagnostics, line) })
	observe(guacd.Instruction{Opcode: "size", Args: []string{"1388", "1038", "96"}}, false)
	observe(guacd.Instruction{Opcode: "mouse", Args: []string{"10", "20", "0"}}, false)
	observe(guacd.Instruction{Opcode: "size", Args: []string{"1388", "1038", "96"}}, true)
	if !reflect.DeepEqual(diagnostics, []string{"browser -> size received 1388x1038", "browser -> size forwarded 1388x1038"}) {
		t.Fatalf("diagnostics=%q", diagnostics)
	}
}

func containsDiagnostic(diagnostics []string, want string) bool {
	for _, line := range diagnostics {
		if line == want {
			return true
		}
	}
	return false
}
