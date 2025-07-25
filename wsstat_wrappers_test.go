package wsstat

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureLatency(t *testing.T) {
	msg := "Hello, world!"
	result, response, err := MeasureLatency(echoServerAddrWs, msg, http.Header{})

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.TotalTime, time.Duration(0))
	assert.NotEqual(t, "", response)
	assert.Equal(t, msg, string(response))
}

func TestMeasureLatencyBurst(t *testing.T) {
	var msgs = []string{
		"msg 1",
		"msg 2",
		"msg 3",
	}
	result, responses, err := MeasureLatencyBurst(echoServerAddrWs, msgs, http.Header{})

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.FirstMessageResponse, time.Duration(0))
	assert.Greater(t, result.MessageRTT, time.Duration(0))
	assert.Greater(t, result.TotalTime, time.Duration(0))
	require.NotNil(t, responses)
	assert.Equal(t, msgs, responses)
}

func TestMeasureLatencyJSON(t *testing.T) {
	message := struct {
		Text string `json:"text"`
	}{
		Text: "Hello, world!",
	}
	result, response, err := MeasureLatencyJSON(echoServerAddrWs, message, http.Header{})

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.TotalTime, time.Duration(0))
	assert.NotEqual(t, nil, response)

	responseMap, ok := response.(map[string]any)
	require.True(t, ok, "Response is not a map")
	assert.Equal(t, message.Text, responseMap["text"])
}

func TestMeasureLatencyJSONBurst(t *testing.T) {
	messages := []struct {
		Text string `json:"text"`
	}{
		{Text: "msg 1"},
		{Text: "msg 2"},
		{Text: "msg 3"},
	}

	// Convert messages to a slice of any
	var interfaceMessages []any
	for _, msg := range messages {
		interfaceMessages = append(interfaceMessages, msg)
	}

	result, responses, err := MeasureLatencyJSONBurst(echoServerAddrWs, interfaceMessages, http.Header{})

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.FirstMessageResponse, time.Duration(0))
	assert.Greater(t, result.MessageRTT, time.Duration(0))
	assert.Greater(t, result.TotalTime, time.Duration(0))
	require.NotNil(t, responses)
	for i, response := range responses {
		responseMap, ok := response.(map[string]any)
		require.True(t, ok, "Response is not a map")
		assert.Equal(t, messages[i].Text, responseMap["text"])
	}
}

func TestMeasureLatencyPing(t *testing.T) {
	result, err := MeasureLatencyPing(echoServerAddrWs, http.Header{})

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.TotalTime, time.Duration(0))
	assert.Greater(t, result.MessageRTT, time.Duration(0))
	assert.Greater(t, result.FirstMessageResponse, time.Duration(0))
}

func TestMeasureLatencyPingBurst(t *testing.T) {
	pingCount := 22
	result, err := MeasureLatencyPingBurst(echoServerAddrWs, pingCount, http.Header{})

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.TotalTime, time.Duration(0))
	assert.Greater(t, result.MessageRTT, time.Duration(0))
	assert.Greater(t, result.FirstMessageResponse, time.Duration(0))
	assert.Equal(t, pingCount, result.MessageCount)
}
