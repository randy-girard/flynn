<?php
use Psr\Http\Message\ResponseInterface as Response;
use Psr\Http\Message\ServerRequestInterface as Request;
use Slim\Factory\AppFactory;

require __DIR__ . '/../vendor/autoload.php';

$app = AppFactory::create();
$app->get('/', function (Request $request, Response $response) {
    $body = "Hello from Flynn on port " . getenv("PORT") . " from container " . gethostname() . "\n";
    $response->getBody()->write($body);
    return $response;
});
$app->run();
