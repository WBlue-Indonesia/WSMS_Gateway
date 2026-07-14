/// Channel names must match the Kotlin side (TelephonyHandler).
class Channels {
  static const method = 'wsms/telephony';
  static const events = 'wsms/telephony_events';
}

/// Frame types — mirror server internal/ws/protocol.go.
class FrameType {
  // device -> server
  static const hello = 'hello';
  static const simReport = 'sim_report';
  static const heartbeat = 'heartbeat';
  static const sendAck = 'send_ack';
  static const deliveryReport = 'delivery_report';
  static const cancelAck = 'cancel_ack';
  // server -> device
  static const sendCommand = 'send_command';
  static const cancel = 'cancel';
  static const config = 'config';
  static const ping = 'ping';
}

class AckResult {
  static const accepted = 'accepted';
  static const rejected = 'rejected';
  static const duplicate = 'duplicate';
}

class DeliveryStatus {
  static const sent = 'sent';
  static const delivered = 'delivered';
  static const failed = 'failed';
}
