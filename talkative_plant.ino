#include <avr/interrupt.h>
#include <avr/pgmspace.h>

#include "thirsty.h"
#include "thankyou.h"
#include "wasdying.h"
#define ARRAYSIZE(x)  (sizeof(x) / sizeof((x)[0]))

const int sensorPin = A0;
const int speakerPin = 3;

void setup() {
  Serial.begin(9600);

  // PIN#3/#11: Timer/Counter2
  pinMode(speakerPin, OUTPUT);

  // http://www.atmel.com/Images/doc8161.pdf
  // Non-inverting fast PWM mode, with no prescaling on pin 3.
  // COM2B1:0 ==  10: Non-inverting mode
  // WGM22:0  == 011: Fast PWM mode, 256 cycle (16MHz / 256 == 62.5kHz)
  // CS2:0    == 001: No prescaler (runs at maximum rate, 62.5kHz)
  TCCR2A = _BV(COM2B1) | _BV(WGM21) | _BV(WGM20);
  TCCR2B = _BV(CS20);
  OCR2B = 0;
}

volatile unsigned long lastPlay = 0;
volatile int lastSensorValue = 0;

void playSound(prog_uchar* audio, int audio_length, bool force) {
  // Don't spam too much.
  if (!force && millis() < lastPlay + 600000) {
    return;
  }
  lastPlay = millis();
  for (int i = 0; i < audio_length; i++) {
    OCR2B = pgm_read_byte_near(&audio[i]);
    delayMicroseconds(125);
  }
}

void loop() {
  int sensorValue = analogRead(sensorPin);
  int diff = sensorValue - lastSensorValue;
  delay(1000);
  Serial.println(sensorValue);

  // http://seeedstudio.com/wiki/Grove_-_Moisture_Sensor
  // dry: 0-300
  // humid: 300-700
  if (sensorValue < 200) {
    playSound(thirsty, ARRAYSIZE(thirsty), abs(diff) > 100);
  } else if (lastSensorValue < 50) {
    playSound(wasdying, ARRAYSIZE(wasdying), true);
  } else if (diff > 100) {
    playSound(thankyou, ARRAYSIZE(thankyou), true);
  }

  lastSensorValue = sensorValue;
}
