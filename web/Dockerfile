# Web image: build the static site with Node, then serve it with nginx, which also forwards /search to the
# API service.
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# Runs nginx as a non-root user, listening on 8080. The nginx image fills ${API_URL} in the template at
# startup.
FROM nginxinc/nginx-unprivileged:alpine
ENV API_URL=http://api:8080
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
