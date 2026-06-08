// Ready-made compose project scaffolds, offered when creating a project. Each
// template's files are written into the new project after it's created.

export interface ProjectTemplate {
  id: string;
  name: string;
  description: string;
  files: { path: string; content: string }[];
}

const nginxStatic: ProjectTemplate = {
  id: "nginx-static",
  name: "Nginx — static site",
  description: "Serves ./html on :8080",
  files: [
    {
      path: "compose.yml",
      content: `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - ./html:/usr/share/nginx/html:ro
    restart: unless-stopped
`,
    },
    {
      path: "html/index.html",
      content: `<!doctype html>
<html lang="en">
  <head><meta charset="utf-8"><title>Hello</title></head>
  <body><h1>It works! 🐳</h1></body>
</html>
`,
    },
  ],
};

const nginxPhp: ProjectTemplate = {
  id: "nginx-php",
  name: "Nginx + PHP-FPM",
  description: "PHP served through nginx on :8080",
  files: [
    {
      path: "compose.yml",
      content: `services:
  web:
    image: nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - ./app:/var/www/html:ro
      - ./nginx/default.conf:/etc/nginx/conf.d/default.conf:ro
    depends_on:
      - php
    restart: unless-stopped
  php:
    image: php:8.3-fpm-alpine
    volumes:
      - ./app:/var/www/html
    restart: unless-stopped
`,
    },
    {
      path: "nginx/default.conf",
      content: `server {
    listen 80;
    server_name _;
    root /var/www/html;
    index index.php index.html;

    location ~ \\.php$ {
        fastcgi_pass php:9000;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
    }
}
`,
    },
    {
      path: "app/index.php",
      content: `<?php
echo "Hello from PHP " . PHP_VERSION . "\\n";
`,
    },
  ],
};

const postgresAdminer: ProjectTemplate = {
  id: "postgres-adminer",
  name: "Postgres + Adminer",
  description: "Database with a web admin UI on :8081",
  files: [
    {
      path: "compose.yml",
      content: `services:
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: \${POSTGRES_USER}
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD}
      POSTGRES_DB: \${POSTGRES_DB}
    volumes:
      - pgdata:/var/lib/postgresql/data
    restart: unless-stopped
  adminer:
    image: adminer
    ports:
      - "8081:8080"
    depends_on:
      - db
    restart: unless-stopped

volumes:
  pgdata:
`,
    },
    {
      path: ".env",
      content: `POSTGRES_USER=app
POSTGRES_PASSWORD=change-me
POSTGRES_DB=app
`,
    },
  ],
};

const nodeApp: ProjectTemplate = {
  id: "node",
  name: "Node app (built image)",
  description: "Dockerfile-built Node service on :3000",
  files: [
    {
      path: "compose.yml",
      content: `services:
  app:
    build: .
    ports:
      - "3000:3000"
    restart: unless-stopped
`,
    },
    {
      path: "Dockerfile",
      content: `FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install --omit=dev
COPY . .
EXPOSE 3000
CMD ["node", "server.js"]
`,
    },
    {
      path: "package.json",
      content: `{
  "name": "app",
  "version": "1.0.0",
  "main": "server.js"
}
`,
    },
    {
      path: "server.js",
      content: `const http = require("http");
const port = process.env.PORT || 3000;
http
  .createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/plain" });
    res.end("Hello from Docker Commander! 🐳\\n");
  })
  .listen(port, () => console.log("listening on " + port));
`,
    },
  ],
};

export const PROJECT_TEMPLATES: ProjectTemplate[] = [nginxStatic, nginxPhp, postgresAdminer, nodeApp];
