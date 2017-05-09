package gdb

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// NotificationCallback is a callback used to report the notifications that GDB
// send asynchronously through the MI2 interface. Responses to the commands are
// not part of these notifications. The notification generic object contains the
// notification sent by GDB.
type NotificationCallback func(notification map[string]interface{})

// Send - Same as SendWithContext, with the Background context.
func (gdb *Gdb) Send(operation string, arguments ...string) (map[string]interface{}, error) {
	return gdb.SendWithContext(context.Background(), operation, arguments...)
}

// SendWithContext issues a command to GDB. Operation is the name of the MI2 command to
// execute without the leading "-" (this means that it is impossible send a CLI
// command), arguments is an optional list of arguments, in GDB parlance the can
// be: options, parameters or "--". It returns a generic object which represents
// the reply of GDB or an error in case the command cannot be delivered to GDB.
func (gdb *Gdb) SendWithContext(ctx context.Context, operation string, arguments ...string) (map[string]interface{}, error) {
	// atomically increase the sequence number and queue a pending command
	pending := make(chan map[string]interface{}, 1)
	gdb.mutex.Lock()
	sequence := strconv.FormatInt(gdb.sequence, 10)
	gdb.pending[sequence] = pending
	gdb.sequence++
	gdb.mutex.Unlock()

	// prepare the command
	buffer := bytes.NewBufferString(fmt.Sprintf("%s-%s", sequence, operation))
	for _, argument := range arguments {
		buffer.WriteByte(' ')
		// quote the argument only if needed because GDB interprets un/quoted
		// values differently in some contexts, e.g., when the value is a
		// number('1' vs '"1"') or an option ('--thread' vs '"--thread"')
		if strings.ContainsAny(argument, "\a\b\f\n\r\t\v\\'\"") {
			argument = strconv.Quote(argument)
		}
		buffer.WriteString(argument)
	}
	buffer.WriteByte('\n')

	// send the command
	if _, err := gdb.Stdin.Write(buffer.Bytes()); err != nil {
		return nil, err
	}

	// wait for a response
	select {
	case result := <-pending:
		gdb.mutex.Lock()
		delete(gdb.pending, sequence)
		gdb.mutex.Unlock()
		return result, nil
	case <-ctx.Done():
		gdb.mutex.Lock()
		delete(gdb.pending, sequence)
		gdb.mutex.Unlock()
		return nil, ctx.Err()
	}

}

func (gdb *Gdb) recordReader() {

	scanner := bufio.NewScanner(gdb.stdout)
	for scanner.Scan() {
		// scan the GDB output one line at a time skipping the GDB terminator
		line := scanner.Text()
		if line == terminator {
			continue
		}

		// parse the line and distinguish between command result and
		// notification
		record := parseRecord(line)
		sequence, isResult := record[sequenceKey]
		isResult = isResult && (sequence != "")
		if isResult {
			// if it is a result record remove the sequence field and complete
			// the pending command
			delete(record, sequenceKey)
			gdb.mutex.RLock()
			pending, ok := gdb.pending[sequence.(string)]
			gdb.mutex.RUnlock()
			if ok {
				pending <- record
			} // TODO: LOG if not OK
		} else {
			if gdb.onNotification != nil {
				gdb.onNotification(record)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
	gdb.recordReaderDone <- true
}
