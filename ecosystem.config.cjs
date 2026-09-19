module.exports = {
  apps: [
    {
      name: "zealish-router-app",
      cwd: "./apps/dashboard",
      script: "npm",
      args: "start",
      interpreter: "none",
      env: {
        NODE_ENV: "production",
        PORT: 18888,
      },
      instances: 1,
      autorestart: true,
      watch: false,
      max_memory_restart: "512M",
      time: true,
    },
  ],
}
