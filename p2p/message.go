package p2p

import (
	"bufio"
	"context"
	"fmt"

	net "github.com/libp2p/go-libp2p-net"
	multicodec "github.com/multiformats/go-multicodec"
	json "github.com/multiformats/go-multicodec/json"
)

// Message is a serializable/encodable object that we will send
// on a Stream.
type Message struct {
	Msg    []byte
	HangUp bool
}

// streamWrap wraps a libp2p stream. We encode/decode whenever we
// write/read from a stream, so we can just carry the encoders
// and bufios with us
type WrappedStream struct {
	stream net.Stream
	enc    multicodec.Encoder
	dec    multicodec.Decoder
	w      *bufio.Writer
	r      *bufio.Reader
}

// wrapStream takes a stream and complements it with r/w bufios and
// decoder/encoder. In order to write raw data to the stream we can use
// wrap.w.Write(). To encode something into it we can wrap.enc.Encode().
// Finally, we should wrap.w.Flush() to actually send the data. Handling
// incoming data works similarly with wrap.r.Read() for raw-reading and
// wrap.dec.Decode() to decode.
func WrapStream(s net.Stream) *WrappedStream {
	reader := bufio.NewReader(s)
	writer := bufio.NewWriter(s)
	// This is where we pick our specific multicodec. In order to change the
	// codec, we only need to change this place.
	// See https://godoc.org/github.com/multiformats/go-multicodec/json
	dec := json.Multicodec(false).Decoder(reader)
	enc := json.Multicodec(false).Encoder(writer)
	return &WrappedStream{
		stream: s,
		r:      reader,
		w:      writer,
		enc:    enc,
		dec:    dec,
	}
}

// StreamHandler handles connections to this node
func (qn *QriNode) MessageStreamHandler(s net.Stream) {
	defer s.Close()
	handleStream(WrapStream(s))
}

// SendMessage to a given multiaddr
func (qn *QriNode) SendMessage(multiaddr string, msg []byte) (res []byte, err error) {
	peerid, err := qn.PeerIdForMultiaddr(multiaddr)
	if err != nil {
		return
	}

	s, err := qn.Host.NewStream(context.Background(), peerid, ProtocolId)
	if err != nil {
		return
	}
	defer s.Close()

	wrappedStream := WrapStream(s)

	err = sendMessage(&Message{Msg: msg, HangUp: true}, wrappedStream)
	if err != nil {
		return
	}

	reply, err := receiveMessage(wrappedStream)
	if err != nil {
		return
	}

	return reply.Msg, nil
}

// receiveMessage reads and decodes a message from the stream
func receiveMessage(ws *WrappedStream) (*Message, error) {
	var msg Message
	err := ws.dec.Decode(&msg)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// // sendMessage encodes and writes a message to the stream
func sendMessage(msg *Message, ws *WrappedStream) error {
	err := ws.enc.Encode(msg)
	// Because output is buffered with bufio, we need to flush!
	ws.w.Flush()
	return err
}

// handleStream is a for loop which receives and then sends a message
// an artificial delay of 500ms happens in-between.
// When Message.HangUp is true, it exists. This will close the stream
// on one of the sides. The other side's receiveMessage() will error
// with EOF, thus also breaking out from the loop.
func handleStream(ws *WrappedStream) {
	for {
		// Read
		msg, err := receiveMessage(ws)
		if err != nil {
			break
		}
		fmt.Printf("received message: %s", string(msg.Msg))
		if msg.HangUp {
			break
		}

		// Send response
		err = sendMessage(&Message{Msg: []byte("ok"), HangUp: true}, ws)
		if err != nil {
			break
		}
	}
}
