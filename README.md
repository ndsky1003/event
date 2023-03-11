### 基于观察者模式
> 解决多服务器的配置文件检查问题

#### usage
1. 启动服务器
```go
event.NewServer().Listen(":8080")
```

2. 1号监听者
```go
	client := event.Dial(":8080", options.Client().SetName("client"))
	//普通模式
	if err := client.On("db_op", func(a int, b string) error {
		//doAction
		return nil
	}); err != nil {
		fmt.Println(err)
	}
	//正则模式
	if err := client.On("/db_[a-z]+/", func(a int, b string, c *Person) error {
		//doAction
		return nil
	}); err != nil {
		fmt.Println(err)
	}
	if err := client.Emit("db_op", i, "1333", &Person{
		Name: "飞毛腿1号",
		Age:  10 + i,
	}); err != nil {
		logrus.Error(err)
	}
	select {}
```

3. 2号监听者
```go
	client := event.Dial(":8080", options.Client().SetName("client"))
	if err := client.On("/db_[a-z]+/", func(a int, b string, c *Person) error {
		//doSomethings
		return nil
	}); err != nil {
		fmt.Println(err)
	}
	select {}

```
