package main

import (
	"errors"

	"os/signal"
	"syscall"

	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gorilla/websocket"

	"context"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

type SensorData struct {
	Id          primitive.ObjectID `json:"_id,omitempty" bson:"_id,omitempty"`
	Temperature float64            `json:"temperature" bson:"temperature"`
	Humidity    float64            `json:"humidity" bson:"humidity"`
	Timestamp   time.Time          `json:"timestamp" bson:"timestamp"`
}

type SensorDataPayload struct {
	Temperature float64 `json:"temperature" binding:"required"`
	Humidity    float64 `json:"humidity" binding:"required"`
}

type SensorDataRequest struct {
	Payload      SensorDataPayload
	Ctx          context.Context
	ResponseChan chan SensorDataResponse
}
type SensorDataResponse struct {
	InsertedId *InsertedId
	Err        error
}
type InsertedId primitive.ObjectID

var SensorDataPayloads = make(chan SensorDataRequest)

var clients []*websocket.Conn
var lock sync.Mutex

var websocketUpgrader = &websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var logger *zap.Logger = func() *zap.Logger {
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	return logger
}()

func addSensorData(ctx context.Context, mc *mongo.Collection, data *SensorData) (primitive.ObjectID, error) {
	res, err := mc.InsertOne(ctx, data)
	if err != nil {
		return primitive.NilObjectID, err
	}
	insertedId, ok := res.InsertedID.(primitive.ObjectID)
	if !ok {
		return primitive.NilObjectID, errors.New("failed to extract _id from inserted document")
	}
	return insertedId, nil
}

func getSensorData(ctx context.Context, mc *mongo.Collection, id string) (*SensorData, error) {
	var data *SensorData
	objId, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, err
	}
	err = mc.FindOne(ctx, bson.M{"_id": objId}).Decode(&data)
	if err != nil {
		return nil, err
	}
	return data, nil
}
func getAllSensorData(ctx context.Context, mc *mongo.Collection) ([]*SensorData, error) {
	var data []*SensorData
	cursor, err := mc.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(100))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	err = cursor.All(ctx, &data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func broadcastAllSensorData(ctx context.Context, mc *mongo.Collection, ws *websocket.Conn) error {
	data, err := getAllSensorData(ctx, mc)
	if err != nil {
		logger.Error("error retrieving all sensor data", zap.Error(err))
		return err
	}
	lock.Lock()
	defer lock.Unlock()

	if err := ws.WriteJSON(gin.H{"message": "successfully retrieved sensor data", "data": data}); err != nil {
		if closeErr := ws.Close(); closeErr != nil {
			return closeErr
		}
	}

	return nil
}

func broadcastSensorData(ctx context.Context, mc *mongo.Collection, id string) error {
	data, err := getSensorData(ctx, mc, id)
	if err != nil {
		logger.Error("error retrieving sensor data", zap.Error(err))
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	for _, ws := range clients {
		if err := ws.WriteJSON(gin.H{"message": "new sensor data", "data": data}); err != nil {
			if closeErr := ws.Close(); closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
}

func sendSensorData(ctx context.Context, mc *mongo.Collection, payload SensorDataPayload) (InsertedId, error) {
	data := &SensorData{
		Temperature: payload.Temperature,
		Humidity:    payload.Humidity,
		Timestamp:   time.Now(),
	}
	insertedId, err := addSensorData(ctx, mc, data)
	if err != nil {
		return InsertedId{}, err
	}
	if err := broadcastSensorData(ctx, mc, insertedId.Hex()); err != nil {
		return InsertedId{}, err
	}
	return InsertedId(insertedId), nil
}

func main() {
	defer logger.Sync()
	if err := godotenv.Load(".env"); err != nil {
		logger.Fatal("Error loading .env file")
	}

	DBURI := os.Getenv("DB_URI")
	if DBURI == "" {
		logger.Fatal("$DB_URI must be set")
	}

	mainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dbClient, err := mongo.Connect(mainCtx, options.Client().ApplyURI(DBURI))
	if err != nil {
		logger.Fatal("error connecting to MongoDB", zap.Error(err))
	}
	if err := dbClient.Ping(mainCtx, nil); err != nil {
		logger.Fatal("failed to ping MongoDB", zap.String("error: ", err.Error()))
	}
	logger.Info("mongodb connected")
	sensorDB := dbClient.Database("sensor-project")
	sensorCollection := sensorDB.Collection("sensor-data")

	go func() {
		for req := range SensorDataPayloads {
			res := SensorDataResponse{}
			insertedId, err := sendSensorData(req.Ctx, sensorCollection, req.Payload)
			if err != nil {
				logger.Error("error sending sensor data", zap.Error(err))
				res.Err = err
			} else {
				res.InsertedId = &insertedId
			}
			req.ResponseChan <- res
		}
	}()

	r := gin.Default()
	r.LoadHTMLFiles("./data.html")
	r.Use(func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		c.Next()
	})
	r.NoRoute(func(ctx *gin.Context) {
		logger.Error("endpoint not found", zap.String("path", ctx.Request.URL.Path))
		ctx.JSON(404, gin.H{"error": "endpoint not found"})
	})
	r.GET("/", func(c *gin.Context) {
		logger.Info("welcome to iot sensor project api", zap.String("status", "ok"))
		c.JSON(http.StatusOK, gin.H{"data": "welcome to iot sensor project api"})
	})
	r.GET("/health", func(ctx *gin.Context) {
		logger.Info("health check", zap.String("status", "ok"))
		ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.POST("/sensor", func(c *gin.Context) {
		var payload SensorDataPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		responseChan := make(chan SensorDataResponse, 1) //1 will prevent blocking

		ctx := c.Request.Context()
		SensorDataPayloads <- SensorDataRequest{Payload: payload, Ctx: ctx, ResponseChan: responseChan}
		select {
		case response := <-responseChan:
			if response.Err != nil {
				logger.Error("error sending sensor data", zap.Error(response.Err))
				c.JSON(http.StatusInternalServerError, gin.H{"error": response.Err.Error()})
			} else if response.InsertedId != nil {
				logger.Info("sensor data received", zap.String("inserted_id", primitive.ObjectID(*response.InsertedId).Hex()))
				c.JSON(http.StatusOK, gin.H{"message": "sensor data received", "inserted_id": primitive.ObjectID(*response.InsertedId).Hex()})
			}
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				logger.Error("timeout or context cancelled", zap.Error(ctx.Err()))
				c.JSON(http.StatusRequestTimeout, gin.H{"error": "request timeout"})
				return
			}
			c.JSON(http.StatusRequestTimeout, gin.H{"error": "request cancelled by client"})
			return
		}
	})
	r.GET("ws/sensor", func(c *gin.Context) {
		wsCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ws, err := websocketUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			logger.Error("error upgrading to websocket", zap.Error(err))
			return
		}
		defer ws.Close()
		logger.Info("websocket client connected", zap.String("remote_addr", ws.RemoteAddr().String()))
		lock.Lock()
		clients = append(clients, ws)
		lock.Unlock()

		defer func() {
			lock.Lock()
			defer lock.Unlock()
			for i, client := range clients {
				if client == ws {
					clients = append(clients[:i], clients[i+1:]...)
					break
				}
			}
		}()
		go broadcastAllSensorData(wsCtx, sensorCollection, ws)
		for {
			messageType, _, err := ws.ReadMessage()
			if err != nil {
				logger.Error("error reading message", zap.Error(err))
				break
			}
			if messageType == websocket.PingMessage {
				logger.Info("pong...")
				if err := ws.WriteMessage(websocket.PongMessage, nil); err != nil {
					logger.Error("error sending pong", zap.Error(err))
					break
				}
			}
		}
	})

	r.GET("/data", func(c *gin.Context) {
		c.Header("Content-Type", "text/html")
		c.HTML(http.StatusOK, "data.html", gin.H{})
	})

	srv := &http.Server{
		Addr:    ":8000",
		Handler: r,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Server start failed", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	<-quit
	logger.Info("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Fatal("Server forced to shutdown:", zap.Error(err))
	}

	logger.Info("Server exiting")
}
