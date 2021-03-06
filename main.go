package main

import (
	"github.com/googollee/go-socket.io"
	"log"
	"cosmicio/cosmicStruct"
	"github.com/ByteArena/box2d"
	"cosmicio/settings"
	"net/http"
	"math"
	"cosmicio/jsexec"
	"math/rand"
	"fmt"
	"sync"
	"os"
	"cosmicio/cosmicDB"
)

//Variables
var currentPlayers=0
var ships = make([]cosmicStruct.PlayerShip,0)
var world = box2d.MakeB2World(box2d.MakeB2Vec2(0,0))
var worldLock = &sync.Mutex{}
var lobby = true
var time = settings.LOBBY_TIME
var dust = make([]cosmicStruct.Dust,0)
var clientDust = make([]cosmicStruct.ClientDust,0)
var particles = make([]*cosmicStruct.Particle,0)


func main() {
	game()
}

func game() {
	cosmicDB.LoadDatabases()
	world.SetContactListener(CollisionListener{})
	log.Println("Loading game server")

	//Server config
	sockets,err := socketio.NewServer(nil)
	//sockets.SetPingInterval(time2.Millisecond*250)
	//sockets.SetPingTimeout(time2.Second*1)
	sockets.SetAllowUpgrades(true)
	if err != nil {
		log.Fatal(err)
	}

	//Connecting
	sockets.On("connection",func(sock socketio.Socket) {
		log.Println("Player connected:" + sock.Id())
		currentPlayers++
		//Creating body definition
		bodydef := box2d.MakeB2BodyDef()
		bodydef.Type = 2
		bodydef.Position.Set(2, 4)
		bodydef.Angle = 0
		bodydef.AngularDamping = settings.PHYSICS_ANGULAR_DUMPING
		bodydef.LinearDamping = settings.PHYSICS_LINEAR_DUMPING

		//Creating collider
		collider := box2d.NewB2PolygonShape()
		collider.SetAsBox(40,120)

		//Creating player
		worldLock.Lock()
		playerShipTmp := cosmicStruct.PlayerShip{
			Id:        currentPlayers,
			Transform: world.CreateBody(&bodydef),
			Username:  "",
			Health:    settings.STARTING_HP,
			SockId:    sock.Id(),
			Alive:     true,
			DustPop:   make(chan int),
			SyncDust:  make(chan bool),
			AddParticle: make(chan cosmicStruct.ClientParticle),
		}
		playerShipTmp.Transform.CreateFixture(collider,1.0).SetRestitution(0.0) //Setting up collider
		worldLock.Unlock()

		//Getting player references
		ships = append(ships, playerShipTmp)

		//Events
		sock.On("movement",func(data cosmicStruct.Movement){
			ship,err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err == nil {
				ships[*ship].Movement = data
			}
		})

		sock.On("username",func(data string){
			ship,err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err == nil {
				log.Println(fmt.Sprintf("Player %s changed username to %s", ships[*ship].Username, data))
				ships[*ship].Username = data
			}
		})

		sock.On("skin",func(data int){
			ship,err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err == nil {
				ships[*ship].SkinId = data
			}
		})

		//Sync functions
		syncUI := func(){
			var title string //Page title
			if lobby {
				title = "Cosmic - Lobby"
			} else {
				title = "Cosmic"
			}

			var alert cosmicStruct.Alert //Alert in the game

			sock.Emit("ui", cosmicStruct.UIData{
				Title: title,
				Lobby: lobby,
				Time:  math.Floor(time),
				Alert: alert,
			})
		}


		syncShips := func(){
			sock.Emit("ships",cosmicStruct.ConvertToClientShips(&ships))
		}

		syncDust := func(){
			ship,err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err == nil {
				for {
					select {
					case <-ships[*ship].SyncDust:
						sock.Emit("cosmicDust",clientDust)
						log.Println("Full dust sync performed")
					}
				}
			}
		}

		dustPop := func(){
			ship,err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err == nil {
				sock.Emit("cosmicDust",clientDust) //Full sync at go-routine creation time
				for {
					select {
					case dustToPop := <-ships[*ship].DustPop:
						sock.Emit("dustRemove", dustToPop)
					}
				}
			}
		}

		syncParticle := func(){
			ship,err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err == nil {
				for {
					select {
					case particle := <-ships[*ship].AddParticle:
						sock.Emit("addParticle",particle)
					}
				}
			}
		}

		//Sync timers
		jsexec.SetInterval(func(){syncUI()},settings.SYNC_UI,true)
		jsexec.SetInterval(func(){syncShips()},settings.SYNC_SHIPS ,true)

		//Async go-routines
		go syncDust()
		go dustPop()
		go syncParticle()

		//Full game state sync
		syncUI()
		syncShips()

		//Disconnect
		sock.On("disconnection",func(sock socketio.Socket) {
			log.Println("Player disconnected:"+sock.Id())
			//Cleanup array
			i, err := cosmicStruct.FindShipBySocketId(&ships,sock.Id())
			if err!=nil{
				panic(err) //Ship not found - something must went really wrong
			}
			ships[*i] = ships[len(ships)-1]
			ships = ships[:len(ships)-1]
		})
	})


	//Server loop
	jsexec.SetInterval(func(){update(float64(settings.SERVER_BEAT)/1000)},settings.SERVER_BEAT,false)

	http.Handle("/socket.io/",sockets)
	http.Handle("/",http.FileServer(http.Dir("./local")))

	//SSL server
	if len(os.Args)>2 {
		log.Fatal(http.ListenAndServeTLS(":443",os.Args[1],os.Args[2],nil))
	} else {
		log.Println("Server ready")
		log.Fatal(http.ListenAndServe(":3000", nil))
	}
}

func update(deltaTime float64) {
	if !lobby{ //Game-only logic
		updatePosition(deltaTime)
	}
	updateTime(deltaTime)
}

func updateTime(deltaTime float64){
	time -= deltaTime
	if time < 0{
		if lobby{
			//Pre-game operations
			time =settings.GAME_TIME
			for i,_ := range ships {ships[i].CleanTurn()} //Clean-up ships
			lobby = !lobby
			generateDust()
			log.Println("Game started")
		} else {
			//Post-game cleanup
			time = settings.LOBBY_TIME
			lobby = !lobby
			//Dust cleanup
			for _,dust := range dust{
				world.DestroyBody(dust.Transform)
			}
			dust= dust[:0]
			//Save hi-scores in database
			cosmicDB.UpdateHighscores(&ships)
			log.Println("Game ended")
		}
	}
}

func updatePosition(deltaTime float64){
	//Ships
	for i,value := range ships {

		forceDirection := value.Transform.GetWorldVector(box2d.MakeB2Vec2(1,0)) //Forward vector
		force := box2d.B2Vec2CrossScalarVector(settings.PHYSICS_FORCE,forceDirection) //Forward force

		//Input movement handling
		if value.Movement.Up {value.Transform.SetLinearVelocity(force)}
		//if value.Movement.Down {value.Transform.ApplyForce(nforce,point, true)}
		if value.Movement.Left {value.Transform.SetAngularVelocity(settings.PHYSICS_ROTATION_FORCE*-1)}
		if value.Movement.Right {value.Transform.SetAngularVelocity(settings.PHYSICS_ROTATION_FORCE)}
		//Shooting
		if value.Movement.Shoot {
			laserShot(value.Transform.GetPosition(),value.Transform.GetAngle(),&i)
			ships[i].Movement.Shoot=false
		}
	}
	//Particles
	for i,_ := range particles {
		if i >= len(particles) {break}
		particles[i].Lifetime -= deltaTime //Remove deltaTime from particle lifetime
		if particles[i].Lifetime <= 0 { //If particle should be now 'dead'
			//Remove particle body
			worldLock.Lock()
			world.DestroyBody(particles[i].Transform)
			worldLock.Unlock()
			//Remove particle from array
			particles[i] = particles[len(particles)-1]
			particles = particles[:len(particles)-1]
		}
	}
	worldLock.Lock()
	world.Step(deltaTime,10,10) //Physics update
	worldLock.Unlock()
}

func generateDust(){
	for i := 0; i < settings.AMOUNT_OF_DUST; i++ {
		//Position generation
		x := rand.Float64() * (500 * settings.MAP_SIZE - -500 * settings.MAP_SIZE) + -500 * settings.MAP_SIZE
		y := rand.Float64() * (500 * settings.MAP_SIZE - -500 * settings.MAP_SIZE) + -500 * settings.MAP_SIZE
		//Dust physics body definition
		bodydef := box2d.MakeB2BodyDef()
		bodydef.Type = 0
		bodydef.Position.Set(x, y)
		bodydef.Angle = 0
		//Dust collider
		shape := box2d.MakeB2CircleShape()
		shape.SetRadius(5)
		//Add to world
		worldLock.Lock()
		dust = append(dust,cosmicStruct.Dust{
			Transform: world.CreateBody(&bodydef),
		})
		worldLock.Unlock()
		fixture := dust[i].Transform.CreateFixture(shape,0.0)
		fixture.SetSensor(true)
	}
	fullsyncClientDust()
}

func updateClientDust(){
	clientDust = cosmicStruct.GenerateClientDust(&dust)
}

func fullsyncClientDust(){
	updateClientDust()
	for i,_ :=range ships {
		ships[i].SyncDust <- true
	}
}

func popClientDust(dustId int){
	updateClientDust()
	for i,_ :=range ships {
		ships[i].DustPop <- dustId
	}
	log.Println("Dust removed:",dustId)
}

func sendParticleToClient(particle *cosmicStruct.Particle){
	clientParticle := particle.ToClientParticle()
	for i,_ :=range ships {
		ships[i].AddParticle <- clientParticle
	}
}

func laserShot(position box2d.B2Vec2,angle float64,ownerIndex *int){
	//Body definition
	bodyDef := box2d.MakeB2BodyDef()
	bodyDef.Position = position
	bodyDef.Angle = angle
	bodyDef.Bullet = true
	//Collider definition
	shape := box2d.MakeB2CircleShape()
	shape.SetRadius(5)

	worldLock.Lock()
	laser := cosmicStruct.Particle{
		Transform: world.CreateBody(&bodyDef),
		Size: 5,
		Type: 0,
		Lifetime: 5,
		Owner: &ships[*ownerIndex],
	}
	laser.Transform.ApplyLinearImpulseToCenter(box2d.B2Vec2_zero,true) //Apply impulse to laser
	worldLock.Unlock()
	particles = append(particles,&laser)
	sendParticleToClient(&laser)
}

//Contact listener
type CollisionListener struct{}

func (CollisionListener) BeginContact(contact box2d.B2ContactInterface){
	//Get colliding bodies
	bodyA := contact.GetFixtureA().GetBody() //Dynamic body
	bodyB := contact.GetFixtureB().GetBody() //Static body

	res1 := cosmicStruct.FindShipByTransform(&ships,bodyA) //Get ship reference
	if res1 != nil { //Check if it isn't null pointer
		i := cosmicStruct.FindDustByTransform(&dust,bodyB) //Find dust index reference
		if i != nil { //Check if it isn't null pointer to player ship index
			dust = append(dust[:*i], dust[*i+1:]...) //Remove dust from array by index
			ships[*res1].Score++ //Add score to ship
			popClientDust(*i) //Async remove dust from client
		}
	} else {
		res2 := cosmicStruct.FindShipByTransform(&ships,bodyB)
		i := cosmicStruct.FindDustByTransform(&dust,bodyA)
		if i != nil {
			dust = append(dust[:*i], dust[*i+1:]...) //Remove dust from array by index
			ships[*res2].Score++ //Add score to ship
			popClientDust(*i) //Async remove dust from client
		}
	}

}
func (CollisionListener) EndContact(contact box2d.B2ContactInterface){

}

func (CollisionListener) PreSolve(contact box2d.B2ContactInterface,oldManifold box2d.B2Manifold){

}
func (CollisionListener) PostSolve(contact box2d.B2ContactInterface,impulse *box2d.B2ContactImpulse){

}

//
type contactListener struct {}
func (contactListener) ReportFixture(){

}

