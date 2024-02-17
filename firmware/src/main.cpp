#include <ESP8266WiFi.h>
#include <WiFiClientSecure.h>
#include <ESP8266HTTPClient.h>
#include <ArduinoJson.h>
#include <Adafruit_Sensor.h>
#include <DHT.h>

void connectToWifi();
void checkWifiConnection();
void sendSensorData(const char *host, const char *endpoint, const char *fingerprint, int serverPort, String payload);
void blinkLED(int pin, unsigned long interval, unsigned long currentMillis);

const char *ssid = WIFI_SSID;
const char *password = WIFI_PASSWORD;
const char *host = SERVER_HOST;
const int httpsPort = SERVER_PORT;
const char *fingerprint = SERVER_FINGERPRINT;

#define LED_PIN 2
#define DHTPIN 0
#define DHTTYPE DHT22
DHT dht(DHTPIN, DHTTYPE);

unsigned long lastWifiCheckTime = 0;
const unsigned long wifiCheckInterval = 60000;
unsigned long lastMessageTime = 0;
const unsigned long messageInterval = 240000;

unsigned long lastBlinkTime = 0;
bool ledState = HIGH;

WiFiClientSecure client;
HTTPClient http;

bool deviceWifiConnected = false;

void setup()
{
    Serial.begin(9600);
    delay(200);
    dht.begin();
    Serial.println();
    pinMode(LED_PIN, OUTPUT);
    connectToWifi();
}

void loop()
{
    unsigned long currentMillis = millis();
    if (currentMillis - lastWifiCheckTime >= wifiCheckInterval)
    {
        checkWifiConnection();
        lastWifiCheckTime = currentMillis;
    }

    if (deviceWifiConnected && currentMillis - lastMessageTime >= messageInterval)
    {
        float humidity = dht.readHumidity();
        float temperature = dht.readTemperature();

        if (isnan(humidity) || isnan(temperature))
        {
            Serial.println("Failed to read from DHT sensor");
            lastMessageTime = currentMillis;
            return;
        }
        JsonDocument doc;
        doc["temperature"] = humidity;
        doc["humidity"] = temperature;

        String payload;
        serializeJson(doc, payload);
        sendSensorData(host, "/sensor", fingerprint, httpsPort, payload);
        lastMessageTime = currentMillis;
    }

    if (!deviceWifiConnected)
        blinkLED(LED_PIN, 1000, currentMillis);
}

void connectToWifi()
{
    if (WiFi.status() != WL_CONNECTED)
    {
        WiFi.begin(ssid, password);
        deviceWifiConnected = false;
    }
}

void checkWifiConnection()
{
    if (WiFi.status() != WL_CONNECTED)
        connectToWifi();
    else
    {
        deviceWifiConnected = true;
        digitalWrite(LED_PIN, LOW);
    }
}

void sendSensorData(const char *host, const char *endpoint, const char *fingerprint, int serverPort, String payload)
{
    String url = "https://" + String(host) + endpoint;
    if (!deviceWifiConnected)
        return;
    if (!client.connected())
    {
        client.setFingerprint(fingerprint);
        if (!client.connect(host, serverPort))
        {
            digitalWrite(LED_PIN, HIGH);
            return;
        }
    }
    http.begin(client, url);
    http.addHeader("Content-Type", "application/json");
    int httpResponseCode = http.POST(payload);

    if (httpResponseCode > 0)
    {
        digitalWrite(LED_PIN, LOW);
        String response = http.getString();
        if (httpResponseCode >= 200 && httpResponseCode < 300)
            Serial.println("success: " + String(httpResponseCode));
        else
            Serial.println("HTTP error: " + String(httpResponseCode));
        Serial.println(response);
    }
    else
    {
        digitalWrite(LED_PIN, HIGH);
        Serial.println("Error on sending POST: " + String(httpResponseCode));
    }
    Serial.println();
    http.end();
}

void blinkLED(int pin, unsigned long interval, unsigned long currentMillis)
{
    if (currentMillis - lastBlinkTime >= interval)
    {
        lastBlinkTime = currentMillis;
        ledState = !ledState;
        digitalWrite(pin, ledState);
    }
}
